package cli

import (
	"context"
	"errors"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

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
