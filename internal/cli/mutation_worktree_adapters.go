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

func defaultAcquireWorktree(repository git.Repository) (lifecycle.AcquisitionWorktree, error) {
	service, err := git.NewWorkspaceService(git.WorkspaceServiceOptions{RepositoryRoot: repository.Root, ManagedRoot: filepath.Dir(repository.Root)})
	if err != nil {
		return nil, err
	}
	return service, nil
}

func defaultReleaseGit(repository git.Repository) (lifecycle.ReleaseGitter, error) {
	service, err := git.NewWorkspaceService(git.WorkspaceServiceOptions{RepositoryRoot: repository.Root, ManagedRoot: filepath.Dir(repository.Root)})
	if err != nil {
		return nil, err
	}
	return service, nil
}
