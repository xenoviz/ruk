package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
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
	// worktree mutation. Nil selects the acquire resolver that pins an
	// immutable commit in the invoking checkout. Create keeps its own lazy
	// resolver because ruk create has no reuse path and always resolves refs
	// in the invoking checkout.
	StartPointResolver CreateStartPointResolver

	CreateWorkspace func(git.Repository) (CreateWorkspace, error)
	// CreateFence optionally replaces the native per-workspace lifecycle lock
	// used while create mutates and prepares its destination. It is primarily a
	// deterministic embedding seam; nil selects the production lock.
	CreateFence      CreateLifecycleFence
	AcquireWorktree  func(git.Repository) (lifecycle.AcquisitionWorktree, error)
	ReleaseGit       func(git.Repository) (lifecycle.ReleaseGitter, error)
	ReleaseProcesses func() lifecycle.ReleaseProcesser
	PortRegistry     func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error)
	// WorktreeRecorder builds the durable per-repository registry that tracks
	// every worktree Ruk creates. Nil selects the native registry beside the
	// repository state file.
	WorktreeRecorder WorktreeRecorderFactory
}

// heldWorkspaceLocker is used only while acquireRepository already owns the
// workspace handoff lock. Dependency preparation still receives the real
// state store, so state mutations remain protected by the state lock; only
// the nested workspace lock is elided to avoid self-deadlock.
type heldWorkspaceLocker struct{}

func (heldWorkspaceLocker) With(ctx context.Context, _ string, callback func() error) error {
	if callback == nil {
		return errors.New("workspace preparation lock: nil callback")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return callback()
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
	sharedSync.PrimaryFence = defaultPrimaryCheckoutFence
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
	syncRoute = wrapAssignedSyncRoute(syncRoute)

	createRoute := func(ctx context.Context, input CreateCommandInput) (CreateCommandResult, error) {
		// Create holds the workspace handoff lock for the whole operation. The
		// dependency layer normally acquires that same lock, so give it the
		// real state store plus a non-reentrant-lock-eliding adapter while this
		// route owns the fence.
		locker, err := newNativeDirectoryLocker(ctx)
		if err != nil {
			return CreateCommandResult{}, err
		}
		preparationStore := state.NewStore(input.Repository.CommonDir, locker)
		fence := options.CreateFence
		if fence == nil {
			fence = func(fenceCtx context.Context, path string, operation func() error) error {
				lockPath, err := MutationWorkspaceLockPath(input.Repository.CommonDir, path)
				if err != nil {
					return err
				}
				return locker.With(fenceCtx, lockPath, operation)
			}
		}
		factory := options.CreateWorkspace
		if factory == nil {
			factory = defaultCreateWorkspace
		}
		workspace, err := factory(input.Repository)
		if err != nil {
			return CreateCommandResult{}, err
		}
		recorder, err := resolveWorktreeRecorder(ctx, options.WorktreeRecorder, input.Repository)
		if err != nil {
			return CreateCommandResult{}, err
		}
		command := NewCreateCommand(CreateCommandOptions{
			Workspace: recordingCreateWorkspace{inner: workspace, recorder: recorder}, Fence: fence,
			Sync: func(ctx context.Context, request CreateSyncRequest) (SyncCommandResult, error) {
				return syncRoute(ctx, SyncCommandInput{
					Repository: request.Repository, JSON: request.JSON, Emit: false, Output: request.Output,
					Ensure: dependencies.EnsureInput{Store: preparationStore, Locker: heldWorkspaceLocker{}},
				})
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
		Release: releaseRoute, Remove: func(ctx context.Context, input RemoveInput) error {
			return removeRepository(ctx, input, options.WorktreeRecorder)
		},
	}, nil
}

// wrapAssignedSyncRoute snapshots an already-assigned workspace before
// dependency work starts and composes an ID-fenced check into Ensure. The
// dependency layer acquires the workspace lock before invoking BeforePrepare,
// so a release/reassignment that wins after the snapshot cannot make sync
// adopt the replacement assignment. Acquisition and create already provide a
// store plus held-lock adapter; those fenced handoffs bypass this wrapper.
func wrapAssignedSyncRoute(route SyncRouteOperation) SyncRouteOperation {
	return func(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
		if route == nil {
			return SyncCommandResult{}, errors.New("sync command is not configured")
		}
		if input.Ensure.Store != nil || input.Ensure.Locker != nil {
			return route(ctx, input)
		}
		if err := validateRepositoryContext(input.Repository); err != nil {
			return SyncCommandResult{}, err
		}
		locker, err := newNativeDirectoryLocker(ctx)
		if err != nil {
			return SyncCommandResult{}, err
		}
		store := state.NewStore(input.Repository.CommonDir, locker)
		workspacePath := input.Repository.Root
		key, err := state.TreeKey(workspacePath)
		if err != nil {
			return SyncCommandResult{}, err
		}
		snapshot, err := store.Read(ctx)
		if err != nil {
			return SyncCommandResult{}, err
		}
		workspace, managed := snapshot.Workspaces[key]
		if !managed || workspace.Assignment == nil {
			return route(ctx, input)
		}
		if workspace.Lifecycle != state.LifecycleAssigned {
			return route(ctx, input)
		}
		if workspace.OperationID != nil {
			return SyncCommandResult{}, fmt.Errorf("Assignment %s acquisition is still in progress", workspace.Assignment.ID)
		}
		assignmentID := workspace.Assignment.ID
		beforePrepare := input.Ensure.BeforePrepare
		input.Ensure.Store = store
		input.Ensure.Locker = locker
		input.Ensure.BeforePrepare = func() error {
			if err := verifyAssignedSync(ctx, store, assignmentID, workspacePath); err != nil {
				return err
			}
			if beforePrepare != nil {
				return beforePrepare()
			}
			return nil
		}
		return route(ctx, input)
	}
}

func verifyAssignedSync(ctx context.Context, store *state.Store, assignmentID, workspacePath string) error {
	if store == nil {
		return errors.New("sync state store is not configured")
	}
	snapshot, err := store.Read(ctx)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return errors.New("sync state reader returned nil state")
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return err
	}
	workspace, ok := snapshot.Workspaces[key]
	if !ok || workspace.Path != workspacePath || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID || workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil {
		return fmt.Errorf("Assignment %s does not exist or no longer owns %s", assignmentID, workspacePath)
	}
	return nil
}
