package cli

import (
	"context"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

func defaultRenewOperation(ctx context.Context, repository git.Repository, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
	store := state.NewStore(repository.CommonDir, lock.NewDirectoryLocker(lock.Config{}))
	service := lifecycle.New(store, lifecycle.Options{
		Now:   time.Now,
		NewID: func() string { return "unused-by-renew" },
	})
	return service.RenewAssignment(ctx, assignmentID, expiresAt, nil)
}
