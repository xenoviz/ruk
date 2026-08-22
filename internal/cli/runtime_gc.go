package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

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
	recorder, err := resolveWorktreeRecorder(ctx, runtimeOptions.Mutations.WorktreeRecorder, repository)
	if err != nil {
		return lifecycle.GCResult{}, err
	}
	paths := state.StorePaths(repository.CommonDir)
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{
		Reader: store, Lifecycle: service, Release: release, Git: workspace,
		Processes: processes, TreeState: stateTreeDeleter{store: store, recorder: recorder}, Locker: locker, LocksRoot: paths.Locks,
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

type stateTreeDeleter struct {
	store    *state.Store
	recorder WorktreeRecorder
}

func (deleter stateTreeDeleter) DeleteTreeState(ctx context.Context, path string) error {
	key, err := state.TreeKey(path)
	if err != nil {
		return err
	}
	if err := deleter.store.Update(ctx, func(current *state.State) error { delete(current.Trees, key); return nil }); err != nil {
		return err
	}
	if deleter.recorder != nil {
		return deleter.recorder.ForgetWorktree(ctx, path)
	}
	return nil
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
