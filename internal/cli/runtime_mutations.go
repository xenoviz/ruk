package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
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

// MutationAdapters is the production route bundle consumed by Application
// construction. Keeping it separate lets the router remain a narrow
// dispatcher and lets tests replace individual low-level seams.
type MutationAdapters struct {
	Sync    SyncRouteOperation
	Create  CreateRouteOperation
	Acquire AcquireRouteOperation
	Release ReleaseRouteOperation
	Remove  RemoveRouteOperation
}

// MutationAdapterOptions supplies deterministic sources and optional seams
// for embedding applications. Nil factories select native implementations.
type MutationAdapterOptions struct {
	Now   func() time.Time
	NewID func() string
	Sync  SyncRouteOperation
	// StartPointResolver resolves acquire --from before any lifecycle state or
	// worktree mutation. Nil selects the same resolver used by create.
	StartPointResolver CreateStartPointResolver

	CreateWorkspace  func(git.Repository) (CreateWorkspace, error)
	AcquireWorktree  func(git.Repository) (lifecycle.AcquisitionWorktree, error)
	ReleaseGit       func(git.Repository) (lifecycle.ReleaseGitter, error)
	ReleaseProcesses func() lifecycle.ReleaseProcesser
	PortRegistry     func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error)
}

// NewMutationAdapters builds the default mutation routes. It does not alter
// Application or command routing; callers can assign the returned functions to
// cli.Options when production mutation behavior is enabled.
func NewMutationAdapters(options MutationAdapterOptions) (MutationAdapters, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomMutationID
	}

	sharedSync := NewSyncCommand()
	sharedSync.Guard = defaultSharedCheckoutGuard
	defaultSyncRoute := func(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
		if err := validateRepositoryContext(input.Repository); err != nil {
			return SyncCommandResult{}, err
		}
		cfg, err := config.Load(input.Repository.Root)
		if err != nil {
			return SyncCommandResult{}, err
		}
		input.Config = cfg
		return sharedSync.Run(ctx, input)
	}
	syncRoute := options.Sync
	if syncRoute == nil {
		syncRoute = defaultSyncRoute
	}

	createRoute := func(ctx context.Context, input CreateCommandInput) (CreateCommandResult, error) {
		factory := options.CreateWorkspace
		if factory == nil {
			factory = defaultCreateWorkspace
		}
		workspace, err := factory(input.Repository)
		if err != nil {
			return CreateCommandResult{}, err
		}
		command := NewCreateCommand(CreateCommandOptions{
			Workspace: workspace,
			Sync: func(ctx context.Context, request CreateSyncRequest) (SyncCommandResult, error) {
				return syncRoute(ctx, SyncCommandInput{Repository: request.Repository, JSON: request.JSON, Emit: false, Output: request.Output})
			},
		})
		return command.Run(ctx, input)
	}

	acquireRoute := func(ctx context.Context, repository git.Repository, input AcquireInput) (AcquireResult, error) {
		return Acquire(ctx, input, func(ctx context.Context, operationInput AcquireOperationInput) (lifecycle.AcquisitionResult, error) {
			return acquireRepository(ctx, repository, operationInput, now, newID, options, syncRoute)
		})
	}

	releaseRoute := func(ctx context.Context, input ReleaseInput) (ReleaseResult, error) {
		return Release(ctx, input, func(ctx context.Context, repository git.Repository, assignmentID string, force bool) (RepositoryReleaseResult, error) {
			return releaseRepository(ctx, repository, assignmentID, force, now, newID, options)
		})
	}

	return MutationAdapters{
		Sync: syncRoute, Create: createRoute, Acquire: acquireRoute,
		Release: releaseRoute, Remove: func(ctx context.Context, input RemoveInput) error { return removeRepository(ctx, input) },
	}, nil
}

// MutationWorkspaceLockPath returns the shared per-workspace lock path used
// by acquisition, release, and unmanaged removal.
func MutationWorkspaceLockPath(commonDir, workspacePath string) (string, error) {
	if err := validateCommonDir(commonDir); err != nil {
		return "", err
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(state.StorePaths(commonDir).Locks, "workspace-"+key+".lock"), nil
}

func defaultRenewOperation(ctx context.Context, repository git.Repository, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return state.WorkspaceRecord{}, err
	}
	store := state.NewStore(repository.CommonDir, lock.NewDirectoryLocker(lock.Config{}))
	service := lifecycle.New(store, lifecycle.Options{Now: time.Now, NewID: func() string { return "unused-by-renew" }})
	return service.RenewAssignment(ctx, assignmentID, expiresAt, nil)
}

type createWorkspaceAdapter struct {
	client git.Client
	root   string
}

func defaultCreateWorkspace(repository git.Repository) (CreateWorkspace, error) {
	if strings.TrimSpace(repository.Root) == "" {
		return nil, errors.New("repository root must not be empty")
	}
	return &createWorkspaceAdapter{client: git.NewClient(nil), root: repository.Root}, nil
}

func (adapter *createWorkspaceAdapter) Create(ctx context.Context, destination, branch, startPoint string, detach bool) error {
	return adapter.client.AddWorktree(ctx, adapter.root, destination, branch, startPoint, detach)
}

func (adapter *createWorkspaceAdapter) Remove(ctx context.Context, destination string, force bool) error {
	return adapter.client.RemoveWorktree(ctx, adapter.root, destination, force)
}

type acquisitionWorktreeAdapter struct{ service *git.WorkspaceService }

func (adapter acquisitionWorktreeAdapter) Create(ctx context.Context, destination, branch, startPoint string) error {
	return adapter.service.Create(ctx, destination, branch, startPoint, true)
}

func (adapter acquisitionWorktreeAdapter) Assign(ctx context.Context, destination, branch, startPoint string) error {
	return adapter.service.Assign(ctx, destination, branch, startPoint)
}

// Return is used by acquisition failure fencing to restore a partially
// mutated native worktree. It is intentionally an additional method beyond
// lifecycle.AcquisitionWorktree so test doubles can remain narrow.
func (adapter acquisitionWorktreeAdapter) Return(ctx context.Context, destination string, force bool, projections []string) error {
	return adapter.service.ResetCleanReturn(ctx, destination, force, projections)
}

type releaseGitAdapter struct{ service *git.WorkspaceService }

func (adapter releaseGitAdapter) ResetCleanReturn(ctx context.Context, destination string, force bool, projections []string) error {
	return adapter.service.ResetCleanReturn(ctx, destination, force, projections)
}

func (adapter releaseGitAdapter) Lock(ctx context.Context, destination string) error {
	return adapter.service.Lock(ctx, destination)
}

func defaultAcquireWorktree(repository git.Repository) (lifecycle.AcquisitionWorktree, error) {
	service, err := git.NewWorkspaceService(git.WorkspaceServiceOptions{RepositoryRoot: repository.Root, ManagedRoot: filepath.Dir(repository.Root)})
	if err != nil {
		return nil, err
	}
	return acquisitionWorktreeAdapter{service: service}, nil
}

func defaultReleaseGit(repository git.Repository) (lifecycle.ReleaseGitter, error) {
	service, err := git.NewWorkspaceService(git.WorkspaceServiceOptions{RepositoryRoot: repository.Root, ManagedRoot: filepath.Dir(repository.Root)})
	if err != nil {
		return nil, err
	}
	return releaseGitAdapter{service: service}, nil
}

func acquireRepository(ctx context.Context, repository git.Repository, input AcquireOperationInput, now func() time.Time, newID func() string, options MutationAdapterOptions, syncRoute SyncRouteOperation) (lifecycle.AcquisitionResult, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	resolver := options.StartPointResolver
	if resolver == nil {
		resolver = defaultCreateStartPoint
	}
	startPoint, err := resolver(ctx, repository, input.From, input.Fetch)
	if err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	input.StartPoint = startPoint
	locker := lock.NewDirectoryLocker(lock.Config{})
	store := state.NewStore(repository.CommonDir, locker)
	service := lifecycle.New(store, lifecycle.Options{Now: now, NewID: newID})
	worktreeFactory := options.AcquireWorktree
	if worktreeFactory == nil {
		worktreeFactory = defaultAcquireWorktree
	}
	worktree, err := worktreeFactory(repository)
	if err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	prepare := func(ctx context.Context, root string) (dependencies.EnsureResult, error) {
		repo := repository
		repo.Root = root
		result, err := syncRoute(ctx, SyncCommandInput{Repository: repo, Emit: false})
		if err != nil {
			return dependencies.EnsureResult{}, err
		}
		return dependencies.EnsureResult{Fingerprint: result.Fingerprint, Mode: result.Mode, Reused: result.Reused, AlreadyAttached: result.AlreadyAttached}, nil
	}
	paths := state.StorePaths(repository.CommonDir)
	portFactory := options.PortRegistry
	if portFactory == nil {
		portFactory = defaultPortRegistry
	}
	allocator, _, err := portFactory(store, paths.State)
	if err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	acquisition := lifecycle.NewAcquisitionService(lifecycle.AcquisitionOptions{
		Lifecycle: service, Reader: store, Locker: locker, Worktree: worktree,
		Prepare: prepare, Ports: allocator,
		WorkspacePath: func(context.Context, string) (string, error) { return defaultPoolPath(repository.Root, input.Branch) },
		LockPath: func(path string) string {
			result, _ := MutationWorkspaceLockPath(repository.CommonDir, path)
			return result
		},
		Cleanup: func(ctx context.Context, path string, force bool) error {
			if resetter, ok := worktree.(interface {
				Return(context.Context, string, bool, []string) error
			}); ok {
				return resetter.Return(ctx, path, force, nil)
			}
			return errors.New("acquisition worktree cleanup is not configured")
		},
	})
	if allocator == nil && len(input.Ports) > 0 {
		return lifecycle.AcquisitionResult{}, errors.New("port allocation is not configured")
	}
	return acquisition.Acquire(ctx, lifecycle.AcquireInput{
		Assignment: lifecycle.AssignmentInput{Owner: input.Owner, Hostname: input.Hostname, Branch: input.Branch, ExpiresAt: input.ExpiresAt},
		Branch:     input.Branch, StartPoint: input.StartPoint, PortNames: append([]string(nil), input.Ports...),
	})
}

func releaseRepository(ctx context.Context, repository git.Repository, assignmentID string, force bool, now func() time.Time, newID func() string, options MutationAdapterOptions) (RepositoryReleaseResult, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return RepositoryReleaseResult{}, err
	}
	locker := lock.NewDirectoryLocker(lock.Config{})
	store := state.NewStore(repository.CommonDir, locker)
	service := lifecycle.New(store, lifecycle.Options{Now: now, NewID: newID})
	gitFactory := options.ReleaseGit
	if gitFactory == nil {
		gitFactory = defaultReleaseGit
	}
	workspaceGit, err := gitFactory(repository)
	if err != nil {
		return RepositoryReleaseResult{}, err
	}
	var processes lifecycle.ReleaseProcesser
	if options.ReleaseProcesses == nil {
		processes = defaultReleaseProcesses()
	} else {
		processes = options.ReleaseProcesses()
	}
	if processes == nil {
		return RepositoryReleaseResult{}, errors.New("native release process adapter is unavailable")
	}
	paths := state.StorePaths(repository.CommonDir)
	portFactory := options.PortRegistry
	if portFactory == nil {
		portFactory = defaultPortRegistry
	}
	_, porter, err := portFactory(store, paths.State)
	if err != nil {
		return RepositoryReleaseResult{}, err
	}
	release := lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: processes, Git: workspaceGit, Ports: porter, Locker: locker,
		LocksRoot: paths.Locks,
		LockPath: func(path string) string {
			result, _ := MutationWorkspaceLockPath(repository.CommonDir, path)
			return result
		},
	})
	result, err := release.ReleaseAssignment(ctx, assignmentID, lifecycle.ReleaseOptions{Force: force})
	if err != nil {
		return RepositoryReleaseResult{}, err
	}
	return RepositoryReleaseResult{Workspace: result.Workspace, CleanedProcesses: result.CleanedProcesses}, nil
}

func removeRepository(ctx context.Context, input RemoveInput) error {
	if err := validateRepositoryContext(input.Repository); err != nil {
		return err
	}
	locker := lock.NewDirectoryLocker(lock.Config{})
	store := state.NewStore(input.Repository.CommonDir, locker)
	client := git.NewClient(nil)
	return (RemoveCommand{
		Canonicalize: func(path string) (string, error) { return filepath.EvalSymlinks(path) },
		ReadState: func(ctx context.Context, _ string) (state.State, error) {
			snapshot, err := store.Read(ctx)
			if err != nil {
				return state.State{}, err
			}
			return *snapshot, nil
		},
		WithLock: locker.With,
		LockPath: func(commonDir, path string) string {
			result, _ := MutationWorkspaceLockPath(commonDir, path)
			return result
		},
		Remove: func(ctx context.Context, root, path string, force bool) error {
			return client.RemoveWorktree(ctx, root, path, force)
		},
		DeleteTree: func(ctx context.Context, _, path string) error {
			key, err := state.TreeKey(path)
			if err != nil {
				return err
			}
			return store.Update(ctx, func(current *state.State) error { delete(current.Trees, key); return nil })
		},
	}).Run(ctx, input)
}

func defaultPortRegistry(store *state.Store, statePath string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
	registry, err := ports.NewRegistry(ports.RegistryOptions{})
	if err != nil {
		return nil, nil, err
	}
	probe := ports.NewAvailabilityProbe(func(request ports.BindRequest) (ports.BoundListener, error) {
		listener, err := net.Listen(request.Network, request.Address)
		if err != nil {
			return nil, err
		}
		return netBoundListener{listener: listener}, nil
	})
	allocator := ports.AllocationService{Store: store, Registry: registry, Finder: probe, StatePath: statePath}
	return allocator, registry, nil
}

type netBoundListener struct{ listener net.Listener }

func (listener netBoundListener) Port() int    { return listener.listener.Addr().(*net.TCPAddr).Port }
func (listener netBoundListener) Close() error { return listener.listener.Close() }

type defaultReleaseProcessesAdapter struct{ tracker processpkg.Tracker }

func (adapter defaultReleaseProcessesAdapter) Exists(ctx context.Context, record state.TrackedProcessRecord) (bool, error) {
	return adapter.tracker.Exists(ctx, record)
}

func (adapter defaultReleaseProcessesAdapter) Terminate(ctx context.Context, record state.TrackedProcessRecord, _ bool) (bool, error) {
	if record.GroupID != nil {
		return false, &processpkg.IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("detached process termination adapter is unavailable")}
	}
	alive, err := adapter.tracker.Exists(ctx, record)
	if err != nil || !alive {
		return false, err
	}
	child, err := os.FindProcess(int(record.PID))
	if err != nil {
		return false, err
	}
	if err := child.Kill(); err != nil {
		return false, err
	}
	return true, nil
}

func defaultReleaseProcesses() lifecycle.ReleaseProcesser {
	return defaultReleaseProcessesAdapter{tracker: processpkg.NewTracker()}
}

func defaultPoolPath(repositoryRoot, branch string) (string, error) {
	if repositoryRoot == "" || branch == "" {
		return "", errors.New("repository root and branch are required")
	}
	return filepath.Join(filepath.Dir(repositoryRoot), filepath.Base(repositoryRoot)+"-ruk-"+slugMutation(branch)+"-"+randomMutationID()[:8]), nil
}

func slugMutation(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	value = strings.Trim(builder.String(), "-")
	if value == "" {
		return "workspace"
	}
	return value
}

func randomMutationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		fallback := time.Now().UnixNano()
		for index := range value {
			value[index] = byte(fallback >> (index % 8 * 8))
		}
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func validateCommonDir(commonDir string) error {
	if strings.TrimSpace(commonDir) == "" {
		return errors.New("Git common directory must not be empty")
	}
	if !filepath.IsAbs(commonDir) {
		return errors.New("Git common directory must be absolute")
	}
	return nil
}

func validateRepositoryContext(repository git.Repository) error {
	if strings.TrimSpace(repository.Root) == "" {
		return errors.New("repository root must not be empty")
	}
	return validateCommonDir(repository.CommonDir)
}

func defaultSharedCheckoutGuard(ctx context.Context, repository git.Repository, cfg config.Config) error {
	if !repository.PrimaryCheckout || cfg.SharedCheckoutPolicy == config.Allow {
		return nil
	}
	if err := validateRepositoryContext(repository); err != nil {
		return err
	}
	store := state.NewStore(repository.CommonDir, lock.NewDirectoryLocker(lock.Config{}))
	snapshot, err := store.Read(ctx)
	if err != nil {
		return err
	}
	active := 0
	for _, workspace := range snapshot.Workspaces {
		if workspace.Assignment != nil {
			active++
		}
	}
	if active == 0 || cfg.SharedCheckoutPolicy == config.Warn {
		return nil
	}
	return NewSharedCheckoutError(active)
}
