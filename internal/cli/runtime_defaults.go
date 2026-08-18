package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	ossignal "os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/ports"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

// RuntimeDefaults is the production route bundle for pool maintenance and
// command execution. Options returns an Application-compatible projection.
type RuntimeDefaults struct {
	Mutations MutationAdapters
	Now       func() time.Time
	Warm      WarmRouteOperation
	GC        GCRouteOperation
	Run       RunRouteOperation
	Exec      ExecRouteOperation
	Shell     ShellRouteOperation
}

// Options returns all production mutation and runtime routes for cli.New.
func (defaults RuntimeDefaults) Options() Options {
	return Options{
		Sync: defaults.Mutations.Sync, Create: defaults.Mutations.Create,
		Acquire: defaults.Mutations.Acquire, Release: defaults.Mutations.Release,
		Remove: defaults.Mutations.Remove, Warm: defaults.Warm, GC: defaults.GC,
		Run: defaults.Run, Exec: defaults.Exec, Shell: defaults.Shell, Now: defaults.Now,
	}
}

// RuntimeDefaultsOptions supplies low-level seams for deterministic embedding
// and tests. Nil values select native implementations.
type RuntimeDefaultsOptions struct {
	Now       func() time.Time
	NewID     func() string
	Mutations MutationAdapterOptions
	GitRunner git.CommandRunner

	WarmWorkspace          func(git.Repository) (lifecycle.WarmWorkspaceService, error)
	WarmHeads              func(context.Context, git.Repository) (map[string]string, error)
	WarmTargetHead         func(context.Context, git.Repository, string, bool) (string, error)
	WarmValidateDependency func(context.Context, git.Repository, string, state.TreeRecord) (bool, error)
	GCWorkspace            func(git.Repository) (lifecycle.GCWorkspaceGit, error)

	ExecuteRunner        processpkg.Runner
	ExecuteActivity      ExecuteActivityRunner
	ExecuteSignals       func() (<-chan os.Signal, func())
	ShellTerminal        ShellTerminal
	PrimaryCheckoutFence PrimaryCheckoutFence
}

type runtimeExecutionOwnership uint8

const (
	runtimeExecutionOwnershipUnknown runtimeExecutionOwnership = iota
	runtimeExecutionOwnershipRetained
	runtimeExecutionOwnershipReleased
)

// NewRuntimeDefaults constructs fail-closed production routes. It does not
// discover a repository or mutate state until one of the returned routes runs.
func NewRuntimeDefaults(options RuntimeDefaultsOptions) (RuntimeDefaults, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomMutationID
	}
	mutationOptions := options.Mutations
	if mutationOptions.Now == nil {
		mutationOptions.Now = now
	}
	if mutationOptions.NewID == nil {
		mutationOptions.NewID = newID
	}
	options.Mutations = mutationOptions
	mutations, err := NewMutationAdapters(mutationOptions)
	if err != nil {
		return RuntimeDefaults{}, err
	}
	defaults := RuntimeDefaults{Mutations: mutations, Now: now}
	defaults.Warm = func(ctx context.Context, repository git.Repository, request WarmRequest) (lifecycle.WarmResult, error) {
		return runtimeWarm(ctx, repository, request, now, newID, mutations.Sync, options)
	}
	defaults.GC = func(ctx context.Context, repository git.Repository, request GCRequest) (lifecycle.GCResult, error) {
		return runtimeGC(ctx, repository, request.Options, now, newID, options)
	}
	defaults.Run = func(ctx context.Context, input RunRouteInput) (int, error) {
		return runtimeRun(ctx, input, now, newID, mutations.Sync, options)
	}
	defaults.Exec = func(ctx context.Context, input ExecRouteInput) (int, error) {
		return runtimeExec(ctx, input, now, newID, mutations, options)
	}
	defaults.Shell = func(ctx context.Context, input ShellRouteInput) (ShellResult, error) {
		return runtimeShell(ctx, input, now, newID, mutations, options)
	}
	return defaults, nil
}

func runtimeShell(ctx context.Context, input ShellRouteInput, now func() time.Time, newID func() string, mutations MutationAdapters, options RuntimeDefaultsOptions) (ShellResult, error) {
	if err := validateRepositoryContext(input.Repository); err != nil {
		return ShellResult{}, err
	}
	if mutations.Acquire == nil || mutations.Release == nil {
		return ShellResult{}, errors.New("shell lifecycle operations are not configured")
	}
	var store *state.Store
	var locker *lock.DirectoryLocker
	var lifecycleService *lifecycle.Service
	// Fully injected shell seams do not need native state or process identity.
	// Keep constructing the production state bundle whenever either the native
	// terminal or the native activity runner is still required.
	if options.ShellTerminal == nil || options.ExecuteActivity == nil {
		var err error
		store, locker, lifecycleService, err = runtimeState(ctx, input.Repository, now, newID)
		if err != nil {
			return ShellResult{}, err
		}
	}
	terminal := options.ShellTerminal
	stopShellSignals := func() {}
	if terminal == nil {
		shellSignals, stopSignals := runtimeManagedSignals()
		stopShellSignals = stopSignals
		terminal = NewNativeShellTerminal(ShellTerminalOptions{
			HandoffLocker: locker,
			HandoffPath: func(path string) (string, error) {
				return MutationWorkspaceLockPath(input.Repository.CommonDir, path)
			},
			Validate: func(ctx context.Context, assignmentID, path string) error {
				return verifyRuntimeAssignment(ctx, store, assignmentID, path)
			},
			Register: func(ctx context.Context, assignmentID string, record state.TrackedProcessRecord) error {
				_, err := lifecycleService.AddAssignmentProcess(ctx, assignmentID, record)
				return err
			},
			Remove: func(ctx context.Context, assignmentID string, record state.TrackedProcessRecord) error {
				_, err := lifecycleService.RemoveAssignmentProcess(ctx, assignmentID, record.PID, record.StartedAt)
				return err
			},
			Signals: shellSignals,
		})
	}
	defer stopShellSignals()
	activity := options.ExecuteActivity
	if activity == nil {
		activity = NewActivityRunner(ActivityRunnerOptions{Lifecycle: lifecycleService, Reader: store, Now: now, NewID: newID}).ExecuteActivityRunner()
	}
	service := NewShellService(ShellOptions{
		Acquire: func(ctx context.Context, request AcquireInput) (AcquireResult, error) {
			return mutations.Acquire(ctx, input.Repository, request)
		},
		Terminal: terminal,
		Activity: activity,
		Expiry: func(ctx context.Context, assignmentID, path string) (string, bool) {
			snapshot, err := store.Read(ctx)
			if err != nil || snapshot == nil {
				return "", false
			}
			key, err := state.TreeKey(path)
			if err != nil {
				return "", false
			}
			workspace, ok := snapshot.Workspaces[key]
			if !ok || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
				return "", false
			}
			return workspace.Assignment.ExpiresAt, true
		},
		Release: func(ctx context.Context, assignmentID string) error {
			_, err := mutations.Release(ctx, ReleaseInput{Repository: input.Repository, AssignmentID: assignmentID})
			return err
		},
	})
	result, err := service.Shell(ctx, ShellInput{
		Branch: input.Branch, From: input.From, Fetch: input.Fetch, TTL: input.TTL,
		Owner: input.Owner, Ports: append([]string(nil), input.Ports...), Now: input.Now,
		Environment: runtimeEnvironmentMap(os.Environ()),
		Stdin:       input.Stdin, Stdout: input.Stdout, Stderr: input.Stderr,
	})
	if err != nil {
		return result, err
	}
	if input.Stderr != nil {
		if _, err := fmt.Fprintf(input.Stderr, "Released %s\n", result.Path); err != nil {
			return result, fmt.Errorf("write shell release: %w", err)
		}
	}
	return result, nil
}

func runtimeEnvironmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name != "" {
			result[name] = value
		}
	}
	return result
}

func runtimeState(ctx context.Context, repository git.Repository, now func() time.Time, newID func() string) (*state.Store, *lock.DirectoryLocker, *lifecycle.Service, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return nil, nil, nil, err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	store := state.NewStore(repository.CommonDir, locker)
	return store, locker, lifecycle.New(store, lifecycle.Options{Now: now, NewID: newID}), nil
}

func runtimeWarm(ctx context.Context, repository git.Repository, request WarmRequest, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions) (lifecycle.WarmResult, error) {
	store, locker, service, err := runtimeState(ctx, repository, now, newID)
	if err != nil {
		return lifecycle.WarmResult{}, err
	}
	runner := options.GitRunner
	workspaceFactory := options.WarmWorkspace
	if workspaceFactory == nil {
		workspaceFactory = func(repository git.Repository) (lifecycle.WarmWorkspaceService, error) {
			return git.NewWorkspaceService(git.WorkspaceServiceOptions{RepositoryRoot: repository.Root, ManagedRoot: filepath.Dir(repository.Root), Runner: runner})
		}
	}
	worktree, err := workspaceFactory(repository)
	if err != nil {
		return lifecycle.WarmResult{}, err
	}
	heads := options.WarmHeads
	if heads == nil {
		heads = func(ctx context.Context, repository git.Repository) (map[string]string, error) {
			records, err := git.ListWorktrees(ctx, repository.Root, runner)
			if err != nil {
				return nil, err
			}
			result := make(map[string]string, len(records))
			for _, record := range records {
				result[record.Path] = record.Head
			}
			return result, nil
		}
	}
	targetHead := options.WarmTargetHead
	if targetHead == nil {
		targetHead = func(ctx context.Context, repository git.Repository, requested string, fetch bool) (string, error) {
			startPoint, err := defaultCreateStartPoint(ctx, repository, requested, fetch)
			if err != nil {
				return "", err
			}
			result, err := runGit(ctx, repository.Root, runner, []string{"rev-parse", "--verify", startPoint + "^{commit}"})
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(result.Stdout), nil
		}
	}
	validateDependency := options.WarmValidateDependency
	if validateDependency == nil {
		validateDependency = runtimeValidateDependency
	}
	if syncRoute == nil {
		return lifecycle.WarmResult{}, errors.New("warm dependency preparation is not configured")
	}
	targetFetch := request.Fetch
	if options.WarmTargetHead == nil {
		// The default resolver already handled --fetch while selecting the
		// lifecycle start point; avoid fetching the same ref twice.
		targetFetch = false
	}
	prepare := func(ctx context.Context, root string) (dependencies.EnsureResult, error) {
		repo := repository
		repo.Root = root
		repo.PrimaryCheckout = false
		result, err := syncRoute(ctx, SyncCommandInput{Repository: repo, JSON: request.JSON, Emit: false})
		if err != nil {
			return dependencies.EnsureResult{}, err
		}
		return dependencies.EnsureResult{Fingerprint: result.Fingerprint, Mode: result.Mode, Reused: result.Reused, AlreadyAttached: result.AlreadyAttached}, nil
	}
	warm := lifecycle.NewWarmService(lifecycle.WarmOptions{
		Lifecycle: service, Reader: store, Locker: locker, Worktree: worktree,
		PoolMaintenanceLockPath: state.StorePaths(repository.CommonDir).Locks + string(filepath.Separator) + "pool-maintenance.lock",
		WarmLockPath:            state.StorePaths(repository.CommonDir).Locks + string(filepath.Separator) + "warm.lock",
		WorktreeHeads:           func(ctx context.Context) (map[string]string, error) { return heads(ctx, repository) },
		TargetHead: func(ctx context.Context, startPoint string) (string, error) {
			return targetHead(ctx, repository, startPoint, targetFetch)
		},
		ValidateDependencies: func(ctx context.Context, path string, tree state.TreeRecord) (bool, error) {
			return validateDependency(ctx, repository, path, tree)
		},
		Prepare:       prepare,
		WorkspacePath: func(_ context.Context, index int) (string, error) { return runtimeWarmPath(repository.Root, index) },
	})
	startPoint, err := defaultCreateStartPoint(ctx, repository, request.From, request.Fetch)
	if err != nil {
		return lifecycle.WarmResult{}, err
	}
	return warm.Warm(ctx, lifecycle.WarmInput{Count: request.Count, StartPoint: startPoint})
}

func runtimeValidateDependency(ctx context.Context, repository git.Repository, path string, tree state.TreeRecord) (bool, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return false, err
	}
	manager, err := dependencies.ResolvePackageManager(ctx, path, cfg)
	if err != nil {
		return false, err
	}
	runtimeIdentity, err := dependencies.ResolveRuntimeIdentity(ctx, path, manager)
	if err != nil {
		return false, err
	}
	files, err := git.ListRepositoryFiles(ctx, path, nil)
	if err != nil {
		return false, err
	}
	details, err := dependencies.DependencyFingerprint(dependencies.SourceFingerprintInput{Root: path, Files: files, Manager: manager, Runtime: runtimeIdentity})
	if err != nil {
		return false, err
	}
	return details.Fingerprint == tree.Fingerprint && dependencies.ProjectionIntegrityValid(path, tree.Projections, tree.ProjectionFingerprint), nil
}

func runtimeGC(ctx context.Context, repository git.Repository, options lifecycle.GCOptions, now func() time.Time, newID func() string, runtimeOptions RuntimeDefaultsOptions) (lifecycle.GCResult, error) {
	store, locker, service, err := runtimeState(ctx, repository, now, newID)
	if err != nil {
		return lifecycle.GCResult{}, err
	}
	workspaceFactory := runtimeOptions.GCWorkspace
	if workspaceFactory == nil {
		workspaceFactory = func(repository git.Repository) (lifecycle.GCWorkspaceGit, error) {
			return gcWorkspaceAdapter{repository: repository, runner: runtimeOptions.GitRunner}, nil
		}
	}
	workspace, err := workspaceFactory(repository)
	if err != nil {
		return lifecycle.GCResult{}, err
	}
	release, err := runtimeReleaseService(repository, store, locker, service, runtimeOptions)
	if err != nil {
		return lifecycle.GCResult{}, err
	}
	processFactory := runtimeOptions.Mutations.ReleaseProcesses
	if processFactory == nil {
		processFactory = func() lifecycle.ReleaseProcesser { return processpkg.NewNativeProcessManager() }
	}
	processes := processFactory()
	if processes == nil {
		return lifecycle.GCResult{}, errors.New("native GC process adapter is unavailable")
	}
	paths := state.StorePaths(repository.CommonDir)
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{
		Reader: store, Lifecycle: service, Release: release, Git: workspace,
		Processes: processes, TreeState: stateTreeDeleter{store: store}, Locker: locker, LocksRoot: paths.Locks,
		Canonicalize: func(_ context.Context, path string) (string, error) { return canonicalRuntimePath(path) },
	})
	return gc.Run(ctx, options)
}

// canonicalRuntimePath resolves an existing path through symlinks while also
// supporting state records whose workspace leaf was never created or has
// already been removed. Only a missing leaf is tolerated; unreadable or
// otherwise invalid ancestors still fail closed. Resolving the nearest
// existing ancestor preserves containment checks when an ancestor is a
// symlink, rather than falling back to a purely lexical path.
func canonicalRuntimePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("runtime path must not be blank")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	candidate := absolute
	for {
		canonical, evalErr := filepath.EvalSymlinks(candidate)
		if evalErr == nil {
			canonical, err = filepath.Abs(filepath.Clean(canonical))
			if err != nil {
				return "", err
			}
			suffix, err := filepath.Rel(candidate, absolute)
			if err != nil {
				return "", err
			}
			return filepath.Abs(filepath.Clean(filepath.Join(canonical, suffix)))
		}
		if !errors.Is(evalErr, os.ErrNotExist) {
			return "", evalErr
		}
		if info, lstatErr := os.Lstat(candidate); lstatErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("resolve runtime path %s: dangling symlink", candidate)
			}
		} else if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", lstatErr
		}
		parent := filepath.Dir(candidate)
		if sameRuntimePath(parent, candidate) {
			return "", evalErr
		}
		candidate = parent
	}
}

type stateTreeDeleter struct{ store *state.Store }

func (deleter stateTreeDeleter) DeleteTreeState(ctx context.Context, path string) error {
	key, err := state.TreeKey(path)
	if err != nil {
		return err
	}
	return deleter.store.Update(ctx, func(current *state.State) error { delete(current.Trees, key); return nil })
}

type gcWorkspaceAdapter struct {
	repository git.Repository
	runner     git.CommandRunner
}

func (adapter gcWorkspaceAdapter) IsWorktree(ctx context.Context, path string) (bool, error) {
	records, err := git.ListWorktrees(ctx, adapter.repository.Root, adapter.runner)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if sameRuntimePath(record.Path, path) {
			return true, nil
		}
	}
	return false, nil
}

func (adapter gcWorkspaceAdapter) Unlock(ctx context.Context, path string) error {
	return git.NewClient(adapter.runner).UnlockWorktree(ctx, adapter.repository.Root, path)
}

func (adapter gcWorkspaceAdapter) Remove(ctx context.Context, path string, force bool) error {
	return git.NewClient(adapter.runner).RemoveWorktree(ctx, adapter.repository.Root, path, force)
}

func (adapter gcWorkspaceAdapter) Lock(ctx context.Context, path string) error {
	return git.NewClient(adapter.runner).LockWorktree(ctx, adapter.repository.Root, path)
}

type runtimeReleaseOperation struct {
	service *lifecycle.ReleaseService
	store   *state.Store
}

func (operation runtimeReleaseOperation) ReleaseAssignment(ctx context.Context, assignmentID string, options lifecycle.ReleaseOptions) (lifecycle.ReleaseResult, error) {
	if options.PreservedProjections == nil && options.PreservedProjectionReader == nil && operation.store != nil {
		options.PreservedProjectionReader = func(readCtx context.Context, workspace state.WorkspaceRecord) ([]string, error) {
			key, err := state.TreeKey(workspace.Path)
			if err != nil {
				return nil, err
			}
			snapshot, readErr := operation.store.Read(readCtx)
			if readErr != nil {
				return nil, readErr
			}
			if snapshot == nil {
				return nil, errors.New("release state reader returned nil state")
			}
			if tree, ok := snapshot.Trees[key]; ok && dependencies.ProjectionIntegrityValid(workspace.Path, tree.Projections, tree.ProjectionFingerprint) {
				return append([]string(nil), tree.Projections...), nil
			}
			return nil, nil
		}
	}
	return operation.service.ReleaseAssignment(ctx, assignmentID, options)
}

func runtimeReleaseService(repository git.Repository, store *state.Store, locker *lock.DirectoryLocker, service *lifecycle.Service, options RuntimeDefaultsOptions) (lifecycle.GCReleaseOperation, error) {
	gitFactory := options.Mutations.ReleaseGit
	if gitFactory == nil {
		gitFactory = defaultReleaseGit
	}
	workspaceGit, err := gitFactory(repository)
	if err != nil {
		return nil, err
	}
	processes := options.Mutations.ReleaseProcesses
	if processes == nil {
		processes = func() lifecycle.ReleaseProcesser { return processpkg.NewNativeProcessManager() }
	}
	portFactory := options.Mutations.PortRegistry
	if portFactory == nil {
		portFactory = defaultPortRegistry
	}
	paths := state.StorePaths(repository.CommonDir)
	_, porter, err := portFactory(store, paths.State)
	if err != nil {
		return nil, err
	}
	release := lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: processes(), Git: workspaceGit, Ports: porter,
		Locker: locker, LocksRoot: paths.Locks,
		LockPath: func(path string) string {
			value, _ := MutationWorkspaceLockPath(repository.CommonDir, path)
			return value
		},
	})
	return runtimeReleaseOperation{service: release, store: store}, nil
}

func runtimeRun(ctx context.Context, input RunRouteInput, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions) (int, error) {
	if err := validateRepositoryContext(input.Repository); err != nil {
		return 1, err
	}
	if input.Repository.PrimaryCheckout && !input.AllowSharedCheckout {
		cfg, err := config.Load(input.Repository.Root)
		if err != nil {
			return 1, err
		}
		run := func() (int, error) {
			if guardErr := defaultSharedCheckoutGuard(ctx, input.Repository, cfg); guardErr != nil {
				var warning *SharedCheckoutWarning
				if !errors.As(guardErr, &warning) {
					return 1, guardErr
				}
				if input.Stderr != nil {
					if _, writeErr := fmt.Fprintln(input.Stderr, warning.Error()); writeErr != nil {
						return 1, fmt.Errorf("write shared-checkout warning: %w", writeErr)
					}
				}
			}
			return runtimeExecute(ctx, input.Repository, input.CWD, input.Command, false, input.AllowSharedCheckout, "", now, newID, syncRoute, options)
		}
		if cfg.SharedCheckoutPolicy == config.Warn || cfg.SharedCheckoutPolicy == config.Allow {
			return run()
		}
		fence := options.PrimaryCheckoutFence
		if fence == nil {
			fence = defaultPrimaryCheckoutFence
		}
		var code int
		var runErr error
		fenceErr := fence(ctx, input.Repository, func() error {
			code, runErr = run()
			return runErr
		})
		if fenceErr != nil {
			return code, fenceErr
		}
		return code, runErr
	}
	return runtimeExecute(ctx, input.Repository, input.CWD, input.Command, false, input.AllowSharedCheckout, "", now, newID, syncRoute, options)
}

func runtimeExec(ctx context.Context, input ExecRouteInput, now func() time.Time, newID func() string, mutations MutationAdapters, options RuntimeDefaultsOptions) (int, error) {
	if mutations.Acquire == nil {
		return 1, errors.New("acquire command is not configured")
	}
	acquired, err := mutations.Acquire(ctx, input.Repository, input.Acquire)
	if err != nil {
		return 1, err
	}
	if acquired.AssignmentID == "" || acquired.Path == "" {
		return 1, errors.New("acquire returned an incomplete assignment")
	}
	code, err, expiresAt, ownership := runtimeExecuteWithExpiry(ctx, input.Repository, input.CWD, input.Command, true, input.AllowSharedCheckout, acquired.AssignmentID, now, newID, mutations.Sync, options, acquired.Path)
	if err != nil {
		return code, runtimeExecutionError(acquired, expiresAt, ownership, err)
	}
	return code, nil
}

func runtimeExecute(ctx context.Context, repository git.Repository, cwd string, command []string, execMode, allowShared bool, assignmentID string, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions, paths ...string) (int, error) {
	code, err, _, _ := runtimeExecuteWithExpiry(ctx, repository, cwd, command, execMode, allowShared, assignmentID, now, newID, syncRoute, options, paths...)
	return code, err
}

// runtimeExecuteWithExpiry returns the latest durable assignment expiry along
// with the execution result. Activity keepers may renew the assignment while
// the child is running, so retained errors must use that post-operation value
// instead of the expiry returned by the initial acquire.
func runtimeExecuteWithExpiry(ctx context.Context, repository git.Repository, cwd string, command []string, execMode, allowShared bool, assignmentID string, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions, paths ...string) (int, error, string, runtimeExecutionOwnership) {
	if strings.TrimSpace(cwd) == "" {
		return 1, errors.New("execution working directory must not be empty"), "", runtimeExecutionOwnershipUnknown
	}
	store, locker, service, err := runtimeState(ctx, repository, now, newID)
	if err != nil {
		return 1, err, "", runtimeExecutionOwnershipUnknown
	}
	workspacePath := repository.Root
	if len(paths) > 0 && paths[0] != "" {
		workspacePath = paths[0]
	}
	if assignmentID == "" {
		snapshot, readErr := store.Read(ctx)
		if readErr != nil {
			return 1, readErr, "", runtimeExecutionOwnershipUnknown
		}
		key, keyErr := state.TreeKey(workspacePath)
		if keyErr != nil {
			return 1, keyErr, "", runtimeExecutionOwnershipUnknown
		}
		workspace, managed := snapshot.Workspaces[key]
		if !managed {
			if syncRoute == nil {
				return 1, errors.New("sync command is not configured"), "", runtimeExecutionOwnershipUnknown
			}
			repo := repository
			repo.Root = workspacePath
			if _, syncErr := syncRoute(ctx, SyncCommandInput{Repository: repo, GuardSharedCheckout: false, AllowSharedCheckout: allowShared, Emit: false}); syncErr != nil {
				return 1, syncErr, "", runtimeExecutionOwnershipUnknown
			}
			current, currentErr := store.Read(ctx)
			if currentErr != nil {
				return 1, currentErr, "", runtimeExecutionOwnershipUnknown
			}
			if _, becameManaged := current.Workspaces[key]; becameManaged {
				return 1, fmt.Errorf("Workspace %s became managed during dependency synchronization", workspacePath), "", runtimeExecutionOwnershipUnknown
			}
			runner := options.ExecuteRunner
			if runner.Spawner == nil {
				runner = processpkg.NewRunner()
			}
			result, runErr := runner.Run(ctx, command, processpkg.RunOptions{
				Dir: workspacePath, Env: os.Environ(), Mode: processpkg.Attached,
				Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
			})
			return result.ExitCode, runErr, "", runtimeExecutionOwnershipUnknown
		}
		if workspace.Assignment == nil || workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil {
			return 1, fmt.Errorf("Workspace %s is not assigned", workspacePath), "", runtimeExecutionOwnershipUnknown
		}
		assignmentID = workspace.Assignment.ID
	}
	if syncRoute == nil {
		return 1, errors.New("sync command is not configured"), "", runtimeExecutionOwnershipUnknown
	}
	baseRepository := repository
	baseRepository.Root = workspacePath
	if execMode {
		baseRepository.PrimaryCheckout = false
	}
	synchronize := func(ctx context.Context, expectedID, path string) error {
		repo := baseRepository
		repo.Root = path
		ensure := dependencies.EnsureInput{BeforePrepare: func() error { return verifyRuntimeAssignment(ctx, store, expectedID, path) }}
		result, err := syncRoute(ctx, SyncCommandInput{Repository: repo, Ensure: ensure, GuardSharedCheckout: false, AllowSharedCheckout: allowShared, Emit: false})
		_ = result
		return err
	}
	release := func(ctx context.Context, id string) error {
		operation, releaseErr := runtimeReleaseService(repository, store, locker, service, options)
		if releaseErr != nil {
			return releaseErr
		}
		_, releaseErr = operation.ReleaseAssignment(ctx, id, lifecycle.ReleaseOptions{})
		return releaseErr
	}
	activity := options.ExecuteActivity
	if activity == nil {
		activity = NewActivityRunner(ActivityRunnerOptions{Lifecycle: service, Reader: store, Now: now, NewID: newID}).ExecuteActivityRunner()
	}
	runner := options.ExecuteRunner
	if runner.Spawner == nil {
		runner = processpkg.NewRunner()
	}
	if runner.Forwarder == nil {
		runner.Forwarder = processpkg.NewNativeSignalForwarder()
	}
	execute := NewExecuteService(ExecuteOptions{
		Lifecycle: service, Reader: store, Runner: runner, Synchronize: synchronize,
		Activity: activity, Release: release, HandoffLocker: locker,
		HandoffPath: func(path string) (string, error) {
			return MutationWorkspaceLockPath(repository.CommonDir, path)
		},
	})
	environment := os.Environ()
	snapshot, err := store.Read(ctx)
	if err != nil {
		return 1, err, "", runtimeExecutionOwnershipUnknown
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return 1, err, "", runtimeExecutionOwnershipUnknown
	}
	if workspace, ok := snapshot.Workspaces[key]; ok && workspace.Assignment != nil {
		additions, envErr := ports.BuildEnvironment(workspace.Assignment.Ports)
		if envErr != nil {
			return 1, envErr, "", runtimeExecutionOwnershipUnknown
		}
		for name, value := range additions {
			environment = append(environment, name+"="+value)
		}
	}
	signalSource := options.ExecuteSignals
	if signalSource == nil {
		signalSource = runtimeManagedSignals
	}
	signals, stopSignals := signalSource()
	if stopSignals == nil {
		stopSignals = func() {}
	}
	defer stopSignals()
	result, err := execute.Execute(ctx, ExecuteInput{AssignmentID: assignmentID, WorkspacePath: workspacePath, Command: command, Env: environment, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Mode: processpkg.Detached, Exec: execMode, Signals: signals})
	expiresAt := ""
	ownership := runtimeExecutionOwnershipUnknown
	if assignmentID != "" {
		if current, readErr := store.Read(context.WithoutCancel(ctx)); readErr == nil {
			ownership = runtimeExecutionOwnershipReleased
			if currentWorkspace, ok := current.Workspaces[key]; ok && currentWorkspace.Assignment != nil && currentWorkspace.Assignment.ID == assignmentID {
				expiresAt = currentWorkspace.Assignment.ExpiresAt
				ownership = runtimeExecutionOwnershipRetained
			}
		}
		if result.Released {
			ownership = runtimeExecutionOwnershipReleased
		}
	}
	return result.ExitCode, err, expiresAt, ownership
}

func retainedRuntimeExecutionError(acquired AcquireResult, expiresAt string, err error) error {
	if expiresAt == "" {
		expiresAt = acquired.ExpiresAt
	}
	if retained := RetainedAssignmentFailure(acquired.AssignmentID, acquired.Path, expiresAt, err); retained != nil {
		return retained
	}
	return err
}

func runtimeExecutionError(acquired AcquireResult, expiresAt string, ownership runtimeExecutionOwnership, err error) error {
	if ownership == runtimeExecutionOwnershipReleased {
		return err
	}
	return retainedRuntimeExecutionError(acquired, expiresAt, err)
}

func runtimeManagedSignals() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 2)
	ossignal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals, func() {
		ossignal.Stop(signals)
		close(signals)
	}
}

func verifyRuntimeAssignment(ctx context.Context, store *state.Store, assignmentID, path string) error {
	snapshot, err := store.Read(ctx)
	if err != nil {
		return err
	}
	key, err := state.TreeKey(path)
	if err != nil {
		return err
	}
	workspace, ok := snapshot.Workspaces[key]
	if !ok || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID || workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil {
		return fmt.Errorf("Assignment %s does not exist or no longer owns %s", assignmentID, path)
	}
	return nil
}

func runtimeWarmPath(root string, index int) (string, error) {
	if strings.TrimSpace(root) == "" || index < 0 {
		return "", errors.New("warm workspace path is invalid")
	}
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+"-ruk-warm-"+fmt.Sprint(index)+"-"+randomMutationID()[:8]), nil
}

func runGit(ctx context.Context, cwd string, runner git.CommandRunner, args []string) (git.CommandResult, error) {
	if runner == nil {
		runner = git.OSCommandRunner
	}
	result, err := runner(ctx, cwd, args)
	if err != nil {
		return git.CommandResult{}, err
	}
	if result.ExitCode != 0 {
		return git.CommandResult{}, fmt.Errorf("Git command failed with exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return result, nil
}

func sameRuntimePath(left, right string) bool {
	return sameRuntimePathForOS(left, right, runtime.GOOS)
}

func sameRuntimePathForOS(left, right, goos string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftAbs = filepath.Clean(leftAbs)
	rightAbs = filepath.Clean(rightAbs)
	if goos == "windows" {
		return strings.EqualFold(leftAbs, rightAbs)
	}
	return leftAbs == rightAbs
}
