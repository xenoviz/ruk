package cli

// This file wires the durable worktree registry into every route that creates
// or removes a Ruk worktree, so all worktrees created by Ruk stay tracked per
// Git repository and per folder. Recording happens immediately after the Git
// mutation succeeds and recording failures fail visibly instead of leaving a
// silently untracked worktree.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
	"github.com/xenoviz/ruk/internal/worktrees"
)

// WorktreeRecorder persists which worktree folders Ruk created for one
// repository. state.WorktreeStore is the production implementation.
type WorktreeRecorder interface {
	RecordWorktree(ctx context.Context, path, branch, source string) error
	ForgetWorktree(ctx context.Context, path string) error
}

// WorktreeRecorderFactory builds the registry recorder for one discovered
// repository. Nil factories select the native per-repository registry.
type WorktreeRecorderFactory func(context.Context, git.Repository) (WorktreeRecorder, error)

// RepositoryIndexer upserts a repository into the host-level discovery index.
type RepositoryIndexer interface {
	RecordRepository(ctx context.Context, commonDir, root string) error
}

type indexedWorktreeRecorder struct {
	repo      WorktreeRecorder
	index     RepositoryIndexer
	commonDir string
	root      string
}

func (recorder indexedWorktreeRecorder) RecordWorktree(ctx context.Context, path, branch, source string) error {
	if err := recorder.repo.RecordWorktree(ctx, path, branch, source); err != nil {
		return err
	}
	return recorder.index.RecordRepository(ctx, recorder.commonDir, recorder.root)
}

func (recorder indexedWorktreeRecorder) ForgetWorktree(ctx context.Context, path string) error {
	return recorder.repo.ForgetWorktree(ctx, path)
}

func defaultWorktreeRecorder(ctx context.Context, repository git.Repository) (WorktreeRecorder, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return nil, err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return nil, err
	}
	root := repository.PrimaryRoot
	if strings.TrimSpace(root) == "" {
		root = repository.Root
	}
	index, err := worktrees.NewIndexStore(worktrees.IndexStoreOptions{Locker: locker})
	if err != nil {
		return nil, err
	}
	return indexedWorktreeRecorder{
		repo:      state.NewWorktreeStore(repository.CommonDir, locker, time.Now),
		index:     index,
		commonDir: repository.CommonDir,
		root:      root,
	}, nil
}

func resolveWorktreeRecorder(ctx context.Context, factory WorktreeRecorderFactory, repository git.Repository) (WorktreeRecorder, error) {
	if factory == nil {
		factory = defaultWorktreeRecorder
	}
	recorder, err := factory(ctx, repository)
	if err != nil {
		return nil, err
	}
	if recorder == nil {
		return nil, errors.New("worktree recorder is not configured")
	}
	return recorder, nil
}

// recordingCreateWorkspace tracks worktrees made by the create command. A
// recording failure removes the just-created worktree so create can never
// leave an untracked folder behind.
type recordingCreateWorkspace struct {
	inner    CreateWorkspace
	recorder WorktreeRecorder
}

func (workspace recordingCreateWorkspace) Create(ctx context.Context, destination, branch, startPoint string, detach bool) error {
	if err := workspace.inner.Create(ctx, destination, branch, startPoint, detach); err != nil {
		return err
	}
	if err := workspace.recorder.RecordWorktree(ctx, destination, branch, state.WorktreeSourceCreate); err != nil {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), createRecoveryTimeout)
		cleanupErr := workspace.inner.Remove(cleanupCtx, destination, true)
		cancelCleanup()
		if cleanupErr != nil {
			return errors.Join(err, cleanupErr)
		}
		return err
	}
	return nil
}

func (workspace recordingCreateWorkspace) Remove(ctx context.Context, destination string, force bool) error {
	if err := workspace.inner.Remove(ctx, destination, force); err != nil {
		return err
	}
	return workspace.recorder.ForgetWorktree(ctx, destination)
}

// recordingAcquisitionWorktree tracks worktrees created by acquisition. A
// recording failure fails the acquisition; the managed pool record still
// fences the folder, and applied GC removes both the worktree and its
// registry entry.
type recordingAcquisitionWorktree struct {
	inner    lifecycle.AcquisitionWorktree
	recorder WorktreeRecorder
}

func (worktree recordingAcquisitionWorktree) Create(ctx context.Context, destination, branch, startPoint string) error {
	if err := worktree.inner.Create(ctx, destination, branch, startPoint); err != nil {
		return err
	}
	return worktree.recorder.RecordWorktree(ctx, destination, branch, state.WorktreeSourceAcquire)
}

func (worktree recordingAcquisitionWorktree) Lock(ctx context.Context, destination string) error {
	return worktree.inner.Lock(ctx, destination)
}

func (worktree recordingAcquisitionWorktree) Assign(ctx context.Context, destination, branch, startPoint string) error {
	if err := worktree.inner.Assign(ctx, destination, branch, startPoint); err != nil {
		return err
	}
	// Reused pool slots predating the registry are adopted here; existing
	// entries only refresh their branch and keep their original provenance.
	return worktree.recorder.RecordWorktree(ctx, destination, branch, state.WorktreeSourceAcquire)
}

// Return forwards acquisition failure cleanup so the fenced reset/clean path
// stays available through the recording decorator.
func (worktree recordingAcquisitionWorktree) Return(ctx context.Context, destination string, force bool, projections []string) error {
	resetter, ok := worktree.inner.(interface {
		Return(context.Context, string, bool, []string) error
	})
	if !ok {
		return errors.New("acquisition worktree cleanup is not configured")
	}
	return resetter.Return(ctx, destination, force, projections)
}

// recordingWarmWorkspace tracks detached worktrees created for pool capacity.
type recordingWarmWorkspace struct {
	inner    lifecycle.WarmWorkspaceService
	recorder WorktreeRecorder
}

func (workspace recordingWarmWorkspace) Create(ctx context.Context, destination, branch, startPoint string, detach bool) error {
	if err := workspace.inner.Create(ctx, destination, branch, startPoint, detach); err != nil {
		return err
	}
	return workspace.recorder.RecordWorktree(ctx, destination, branch, state.WorktreeSourceWarm)
}

func (workspace recordingWarmWorkspace) Lock(ctx context.Context, destination string) error {
	return workspace.inner.Lock(ctx, destination)
}
