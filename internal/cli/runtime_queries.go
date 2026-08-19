package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
	"github.com/xenoviz/ruk/internal/statistics"
)

func defaultQueryDependencies() QueryDependencies {
	return QueryDependencies{
		ListWorktrees: func(ctx context.Context, root string) ([]git.WorktreeRecord, error) {
			return git.ListWorktrees(ctx, root, nil)
		},
		ReadState: func(ctx context.Context, commonDir string) (state.State, error) {
			locker, err := newNativeDirectoryLocker(ctx)
			if err != nil {
				return state.State{}, err
			}
			store := state.NewStore(commonDir, locker)
			snapshot, err := store.Read(ctx)
			if err != nil {
				return state.State{}, err
			}
			if snapshot == nil {
				return state.State{}, errors.New("state store returned nil state")
			}
			return *snapshot, nil
		},
		CurrentFingerprint: currentDependencyFingerprint,
		DependenciesPresent: func(ctx context.Context, root string, projections []string) (bool, error) {
			for _, projection := range projections {
				if err := ctx.Err(); err != nil {
					return false, err
				}
				path, err := queryProjectionPath(root, projection)
				if err != nil {
					return false, err
				}
				if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
					return false, nil
				} else if err != nil {
					return false, fmt.Errorf("inspect dependency projection %s: %w", projection, err)
				}
			}
			return len(projections) != 0, nil
		},
		ProjectionsValid: func(ctx context.Context, root string, record state.TreeRecord) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			return dependencies.ProjectionIntegrityValid(root, record.Projections, record.ProjectionFingerprint), nil
		},
		MeasureDisk: func(ctx context.Context, snapshot state.State) (statistics.DiskStatistics, error) {
			return statistics.MeasureDiskStatistics(ctx, snapshot)
		},
	}
}

func currentDependencyFingerprint(ctx context.Context, root string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return "", err
	}
	manager, err := dependencies.ResolvePackageManager(ctx, root, cfg)
	if err != nil {
		return "", err
	}
	runtimeIdentity, err := dependencies.ResolveRuntimeIdentity(ctx, root, manager)
	if err != nil {
		return "", err
	}
	files, err := git.ListRepositoryFiles(ctx, root, nil)
	if err != nil {
		return "", err
	}
	details, err := dependencies.DependencyFingerprint(dependencies.SourceFingerprintInput{
		Root: root, Files: files, Manager: manager, Runtime: runtimeIdentity,
	})
	if err != nil {
		return "", err
	}
	return details.Fingerprint, nil
}

func queryProjectionPath(root, relative string) (string, error) {
	resolvedRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	normalized := strings.ReplaceAll(relative, `\`, "/")
	if normalized == "" || filepath.IsAbs(relative) || strings.HasPrefix(normalized, "/") ||
		(len(normalized) >= 2 && normalized[1] == ':') {
		return "", fmt.Errorf("dependency projection must stay inside the workspace: %s", relative)
	}
	target := filepath.Clean(filepath.Join(resolvedRoot, filepath.FromSlash(normalized)))
	if target == resolvedRoot || !strings.HasPrefix(target, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("dependency projection must stay inside the workspace: %s", relative)
	}
	return target, nil
}

func mergeQueryDependencies(value, defaults QueryDependencies) QueryDependencies {
	if value.ListWorktrees == nil {
		value.ListWorktrees = defaults.ListWorktrees
	}
	if value.ReadState == nil {
		value.ReadState = defaults.ReadState
	}
	if value.CurrentFingerprint == nil {
		value.CurrentFingerprint = defaults.CurrentFingerprint
	}
	if value.DependenciesPresent == nil {
		value.DependenciesPresent = defaults.DependenciesPresent
	}
	if value.ProjectionsValid == nil {
		value.ProjectionsValid = defaults.ProjectionsValid
	}
	if value.MeasureDisk == nil {
		value.MeasureDisk = defaults.MeasureDisk
	}
	return value
}
