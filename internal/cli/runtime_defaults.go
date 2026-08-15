package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	ExecuteRunner   processpkg.Runner
	ExecuteActivity ExecuteActivityRunner
	ShellTerminal   ShellTerminal
}

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
		return runtimeShell(ctx, input, mutations, options)
	}
	return defaults, nil
}

func runtimeShell(ctx context.Context, input ShellRouteInput, mutations MutationAdapters, options RuntimeDefaultsOptions) (ShellResult, error) {
	if err := validateRepositoryContext(input.Repository); err != nil {
		return ShellResult{}, err
	}
	if mutations.Acquire == nil || mutations.Release == nil {
		return ShellResult{}, errors.New("shell lifecycle operations are not configured")
	}
	terminal := options.ShellTerminal
	if terminal == nil {
		terminal = NewNativeShellTerminal(ShellTerminalOptions{})
	}
	service := NewShellService(ShellOptions{
		Acquire: func(ctx context.Context, request AcquireInput) (AcquireResult, error) {
			return mutations.Acquire(ctx, input.Repository, request)
		},
		Terminal: terminal,
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

func runtimeState(repository git.Repository, now func() time.Time, newID func() string) (*state.Store, *lock.DirectoryLocker, *lifecycle.Service, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return nil, nil, nil, err
	}
	locker := lock.NewDirectoryLocker(lock.Config{})
	store := state.NewStore(repository.CommonDir, locker)
	return store, locker, lifecycle.New(store, lifecycle.Options{Now: now, NewID: newID}), nil
}

func runtimeWarm(ctx context.Context, repository git.Repository, request WarmRequest, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions) (lifecycle.WarmResult, error) {
	store, locker, service, err := runtimeState(repository, now, newID)
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
		result, err := syncRoute(ctx, SyncCommandInput{Repository: repo, Emit: false})
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
	files, err := git.ListRepositoryFiles(ctx, path, nil)
	if err != nil {
		return false, err
	}
	details, err := dependencies.DependencyFingerprint(dependencies.SourceFingerprintInput{Root: path, Files: files, Manager: manager})
	if err != nil {
		return false, err
	}
	return details.Fingerprint == tree.Fingerprint && dependencies.ProjectionIntegrityValid(path, tree.Projections, tree.ProjectionFingerprint), nil
}

func runtimeGC(ctx context.Context, repository git.Repository, options lifecycle.GCOptions, now func() time.Time, newID func() string, runtimeOptions RuntimeDefaultsOptions) (lifecycle.GCResult, error) {
	store, locker, service, err := runtimeState(repository, now, newID)
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
	paths := state.StorePaths(repository.CommonDir)
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{
		Reader: store, Lifecycle: service, Release: release, Git: workspace,
		TreeState: stateTreeDeleter{store: store}, Locker: locker, LocksRoot: paths.Locks,
		Canonicalize: func(_ context.Context, path string) (string, error) { return filepath.EvalSymlinks(path) },
	})
	return gc.Run(ctx, options)
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
	if options.PreservedProjections == nil && operation.store != nil {
		snapshot, err := operation.store.Read(ctx)
		if err != nil {
			return lifecycle.ReleaseResult{}, err
		}
		for _, workspace := range snapshot.Workspaces {
			if workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
				continue
			}
			key, err := state.TreeKey(workspace.Path)
			if err != nil {
				return lifecycle.ReleaseResult{}, err
			}
			if tree, ok := snapshot.Trees[key]; ok {
				options.PreservedProjections = append([]string(nil), tree.Projections...)
			}
			break
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
		if err := defaultSharedCheckoutGuard(ctx, input.Repository, cfg); err != nil {
			return 1, err
		}
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
	return runtimeExecute(ctx, input.Repository, input.CWD, input.Command, true, input.AllowSharedCheckout, acquired.AssignmentID, now, newID, mutations.Sync, options, acquired.Path)
}

func runtimeExecute(ctx context.Context, repository git.Repository, cwd string, command []string, execMode, allowShared bool, assignmentID string, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions, paths ...string) (int, error) {
	if strings.TrimSpace(cwd) == "" {
		return 1, errors.New("execution working directory must not be empty")
	}
	store, locker, service, err := runtimeState(repository, now, newID)
	if err != nil {
		return 1, err
	}
	workspacePath := repository.Root
	if len(paths) > 0 && paths[0] != "" {
		workspacePath = paths[0]
	}
	if assignmentID == "" {
		snapshot, readErr := store.Read(ctx)
		if readErr != nil {
			return 1, readErr
		}
		key, keyErr := state.TreeKey(workspacePath)
		if keyErr != nil {
			return 1, keyErr
		}
		workspace, managed := snapshot.Workspaces[key]
		if !managed {
			if syncRoute == nil {
				return 1, errors.New("sync command is not configured")
			}
			repo := repository
			repo.Root = workspacePath
			if _, syncErr := syncRoute(ctx, SyncCommandInput{Repository: repo, GuardSharedCheckout: false, AllowSharedCheckout: allowShared, Emit: false}); syncErr != nil {
				return 1, syncErr
			}
			current, currentErr := store.Read(ctx)
			if currentErr != nil {
				return 1, currentErr
			}
			if _, becameManaged := current.Workspaces[key]; becameManaged {
				return 1, fmt.Errorf("Workspace %s became managed during dependency synchronization", workspacePath)
			}
			runner := options.ExecuteRunner
			if runner.Spawner == nil {
				runner = processpkg.NewRunner()
			}
			result, runErr := runner.Run(ctx, command, processpkg.RunOptions{
				Dir: workspacePath, Env: os.Environ(), Mode: processpkg.Attached,
				Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
			})
			return result.ExitCode, runErr
		}
		if workspace.Assignment == nil || workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil {
			return 1, fmt.Errorf("Workspace %s is not assigned", workspacePath)
		}
		assignmentID = workspace.Assignment.ID
	}
	if syncRoute == nil {
		return 1, errors.New("sync command is not configured")
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
	execute := NewExecuteService(ExecuteOptions{Lifecycle: service, Reader: store, Runner: runner, Synchronize: synchronize, Activity: activity, Release: release})
	environment := os.Environ()
	snapshot, err := store.Read(ctx)
	if err != nil {
		return 1, err
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return 1, err
	}
	if workspace, ok := snapshot.Workspaces[key]; ok && workspace.Assignment != nil {
		additions, envErr := ports.BuildEnvironment(workspace.Assignment.Ports)
		if envErr != nil {
			return 1, envErr
		}
		for name, value := range additions {
			environment = append(environment, name+"="+value)
		}
	}
	result, err := execute.Execute(ctx, ExecuteInput{AssignmentID: assignmentID, WorkspacePath: workspacePath, Command: command, Env: environment, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Mode: processpkg.Detached, Exec: execMode})
	return result.ExitCode, err
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
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}
