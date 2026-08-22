package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

// defaultAcquireStartPoint pins the start point to an immutable commit in the
// invoking checkout before any lifecycle state or worktree mutation. Reuse
// assignment executes Git inside the recycled slot, where worktree-relative
// refs such as HEAD resolve against the slot's stale detached commit; pinning
// in the invoking checkout makes fresh and reused acquisition start from the
// same commit and also fences against the ref moving mid-acquisition.
func defaultAcquireStartPoint(ctx context.Context, repository git.Repository, requested string, fetch bool) (string, error) {
	startPoint, err := defaultCreateStartPoint(ctx, repository, requested, fetch)
	if err != nil {
		return "", err
	}
	result, err := runGit(ctx, repository.Root, nil, []string{"rev-parse", "--verify", startPoint + "^{commit}"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func acquireRepository(ctx context.Context, repository git.Repository, input AcquireOperationInput, now func() time.Time, newID func() string, options MutationAdapterOptions, syncRoute SyncRouteOperation) (lifecycle.AcquisitionResult, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	resolver := options.StartPointResolver
	if resolver == nil {
		resolver = defaultAcquireStartPoint
	}
	startPoint, err := resolver(ctx, repository, input.From, input.Fetch)
	if err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	input.StartPoint = startPoint
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	store := state.NewStore(repository.CommonDir, locker)
	service := lifecycle.New(store, lifecycle.Options{Now: now, NewID: newID})
	worktreeFactory := options.AcquireWorktree
	if worktreeFactory == nil {
		worktreeFactory = defaultAcquireWorktree
	}
	innerWorktree, err := worktreeFactory(repository)
	if err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	recorder, err := resolveWorktreeRecorder(ctx, options.WorktreeRecorder, repository)
	if err != nil {
		return lifecycle.AcquisitionResult{}, err
	}
	worktree := lifecycle.AcquisitionWorktree(recordingAcquisitionWorktree{inner: innerWorktree, recorder: recorder})
	prepare := func(ctx context.Context, root string) (dependencies.EnsureResult, error) {
		repo := repository
		repo.Root = root
		result, err := syncRoute(ctx, SyncCommandInput{
			Repository: repo, JSON: input.JSON,
			Ensure: dependencies.EnsureInput{Store: store, Locker: heldWorkspaceLocker{}},
			Emit:   false,
		})
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
		Lifecycle: service, Reader: store, Locker: locker, PrimaryLocker: locker,
		PrimaryLockPath: primaryCheckoutLockPath(repository.CommonDir), Worktree: worktree,
		PoolMaintenanceLocker:   locker,
		PoolMaintenanceLockPath: filepath.Join(paths.Locks, "pool-maintenance.lock"),
		Prepare:                 prepare, Ports: allocator,
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
		Assignment: lifecycle.AssignmentInput{Owner: input.Owner, Hostname: input.Hostname, Branch: input.Branch, ExpiresAt: input.ExpiresAt, LeaseDurationMinutes: input.LeaseDurationMinutes},
		Branch:     input.Branch, StartPoint: input.StartPoint, PortNames: append([]string(nil), input.Ports...),
	})
}
