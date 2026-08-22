package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

func runtimeWarm(ctx context.Context, repository git.Repository, request WarmRequest, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions) (lifecycle.WarmResult, error) {
	store, locker, service, err := runtimeState(ctx, repository, now, newID)
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
	recorder, err := resolveWorktreeRecorder(ctx, options.Mutations.WorktreeRecorder, repository)
	if err != nil {
		return lifecycle.WarmResult{}, err
	}
	worktree = lifecycle.WarmWorkspaceService(recordingWarmWorkspace{inner: worktree, recorder: recorder})
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
		result, err := syncRoute(ctx, SyncCommandInput{Repository: repo, JSON: request.JSON, Emit: false})
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
	runtimeIdentity, err := dependencies.ResolveRuntimeIdentity(ctx, path, manager)
	if err != nil {
		return false, err
	}
	files, err := git.ListRepositoryFiles(ctx, path, nil)
	if err != nil {
		return false, err
	}
	details, err := dependencies.DependencyFingerprint(dependencies.SourceFingerprintInput{Root: path, Files: files, Manager: manager, Runtime: runtimeIdentity})
	if err != nil {
		return false, err
	}
	return details.Fingerprint == tree.Fingerprint && dependencies.ProjectionIntegrityValid(path, tree.Projections, tree.ProjectionFingerprint), nil
}

func runtimeWarmPath(root string, index int) (string, error) {
	if strings.TrimSpace(root) == "" || index < 0 {
		return "", errors.New("warm workspace path is invalid")
	}
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+"-ruk-warm-"+fmt.Sprint(index)+"-"+randomMutationID()[:8]), nil
}
