package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

func defaultPrimaryCheckoutFence(ctx context.Context, repository git.Repository, callback func() error) error {
	if callback == nil {
		return errors.New("primary checkout fence callback is not configured")
	}
	if err := validateRepositoryContext(repository); err != nil {
		return err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return err
	}
	path := primaryCheckoutLockPath(repository.CommonDir)
	return locker.With(ctx, path, callback)
}

func primaryCheckoutLockPath(commonDir string) string {
	return filepath.Join(state.StorePaths(commonDir).Locks, "primary-checkout.lock")
}

// MutationWorkspaceLockPath returns the shared per-workspace lock path used
// by acquisition, release, and unmanaged removal.
func MutationWorkspaceLockPath(commonDir, workspacePath string) (string, error) {
	if err := validateCommonDir(commonDir); err != nil {
		return "", err
	}
	if strings.TrimSpace(workspacePath) == "" {
		return "", errors.New("workspace path must not be empty")
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(state.StorePaths(commonDir).Locks, "workspace-"+key+".lock"), nil
}

func defaultRenewOperation(ctx context.Context, repository git.Repository, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return state.WorkspaceRecord{}, err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	store := state.NewStore(repository.CommonDir, locker)
	service := lifecycle.New(store, lifecycle.Options{Now: time.Now, NewID: func() string { return "unused-by-renew" }})
	return service.RenewAssignment(ctx, assignmentID, expiresAt, nil)
}

func validateCommonDir(commonDir string) error {
	if strings.TrimSpace(commonDir) == "" {
		return errors.New("Git common directory must not be empty")
	}
	if !filepath.IsAbs(commonDir) {
		return errors.New("Git common directory must be absolute")
	}
	return nil
}

func validateRepositoryContext(repository git.Repository) error {
	if strings.TrimSpace(repository.Root) == "" {
		return errors.New("repository root must not be empty")
	}
	return validateCommonDir(repository.CommonDir)
}

func defaultSharedCheckoutGuard(ctx context.Context, repository git.Repository, cfg config.Config) error {
	if !repository.PrimaryCheckout || cfg.SharedCheckoutPolicy == config.Allow {
		return nil
	}
	if err := validateRepositoryContext(repository); err != nil {
		return err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return err
	}
	store := state.NewStore(repository.CommonDir, locker)
	snapshot, err := store.Read(ctx)
	if err != nil {
		return err
	}
	active := 0
	for _, workspace := range snapshot.Workspaces {
		if workspace.Assignment != nil {
			active++
		}
	}
	if active == 0 {
		return nil
	}
	if cfg.SharedCheckoutPolicy == config.Warn {
		return &SharedCheckoutWarning{ActiveAssignments: active}
	}
	return NewSharedCheckoutError(active)
}
