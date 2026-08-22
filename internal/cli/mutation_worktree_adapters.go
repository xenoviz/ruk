package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
)

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

func (adapter acquisitionWorktreeAdapter) Lock(ctx context.Context, destination string) error {
	return adapter.service.Lock(ctx, destination)
}

func (adapter acquisitionWorktreeAdapter) Assign(ctx context.Context, destination, branch, startPoint string) error {
	return adapter.service.Assign(ctx, destination, branch, startPoint)
}

// Return is used by acquisition failure fencing to restore a partially
// mutated native worktree. It remains an additional method beyond
// lifecycle.AcquisitionWorktree because acquisition only requires locking,
// creation, and branch assignment.
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
