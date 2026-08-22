package cli

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

func releaseRepository(ctx context.Context, repository git.Repository, assignmentID string, force bool, now func() time.Time, newID func() string, options MutationAdapterOptions) (RepositoryReleaseResult, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return RepositoryReleaseResult{}, err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return RepositoryReleaseResult{}, err
	}
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
	result, err := release.ReleaseAssignment(ctx, assignmentID, lifecycle.ReleaseOptions{
		Force: force,
		PreservedProjectionReader: func(readCtx context.Context, _ state.WorkspaceRecord) ([]string, error) {
			snapshot, readErr := store.Read(readCtx)
			if readErr != nil {
				return nil, readErr
			}
			return assignmentProjections(snapshot, assignmentID), nil
		},
	})
	if err != nil {
		return RepositoryReleaseResult{}, err
	}
	return RepositoryReleaseResult{Workspace: result.Workspace, CleanedProcesses: result.CleanedProcesses}, nil
}

func assignmentProjections(snapshot *state.State, assignmentID string) []string {
	if snapshot == nil {
		return nil
	}
	for key, workspace := range snapshot.Workspaces {
		if workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
			continue
		}
		tree, ok := snapshot.Trees[key]
		if !ok {
			return nil
		}
		if !dependencies.ProjectionIntegrityValid(workspace.Path, tree.Projections, tree.ProjectionFingerprint) {
			return nil
		}
		return append([]string(nil), tree.Projections...)
	}
	return nil
}

func removeRepository(ctx context.Context, input RemoveInput, recorderFactory WorktreeRecorderFactory) error {
	if err := validateRepositoryContext(input.Repository); err != nil {
		return err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return err
	}
	store := state.NewStore(input.Repository.CommonDir, locker)
	recorder, err := resolveWorktreeRecorder(ctx, recorderFactory, input.Repository)
	if err != nil {
		return err
	}
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
			if err := store.Update(ctx, func(current *state.State) error { delete(current.Trees, key); return nil }); err != nil {
				return err
			}
			return recorder.ForgetWorktree(ctx, path)
		},
	}).Run(ctx, input)
}
