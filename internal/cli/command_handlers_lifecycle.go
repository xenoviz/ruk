package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

// runAcquire executes acquire against the discovered repository.
func (application *Application) runAcquire(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if application.acquire == nil {
		return 1, errors.New("acquire command is not configured")
	}
	result, err := application.acquire(ctx, repository, AcquireInput{
		Branch: invocation.Branch,
		From:   invocation.From,
		Fetch:  invocation.Fetch,
		TTL:    invocation.TTL,
		Owner:  invocation.Owner,
		Ports:  invocation.Ports,
		JSON:   invocation.JSON,
		Now:    application.now(),
	})
	if err != nil {
		return 1, err
	}
	if _, err := io.WriteString(application.stdout, result.Output); err != nil {
		return 1, NewRetainedAssignmentError(
			result.AssignmentID,
			result.Path,
			result.ExpiresAt,
			fmt.Errorf("write acquire result: %w", err),
		)
	}
	return 0, nil
}

// runRelease executes release and renders its result.
func (application *Application) runRelease(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if application.release == nil {
		return 1, errors.New("release command is not configured")
	}
	result, err := application.release(ctx, ReleaseInput{
		Repository:   repository,
		AssignmentID: invocation.AssignmentID,
		Force:        invocation.Force,
		JSON:         invocation.JSON,
	})
	if err != nil {
		return 1, err
	}
	if _, err := io.WriteString(application.stdout, result.Output); err != nil {
		return 1, fmt.Errorf("write release result: %w", err)
	}
	return 0, nil
}

// runRenew executes one lifecycle renewal.
func (application *Application) runRenew(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	result, err := Renew(ctx, RenewInput{
		AssignmentID: invocation.AssignmentID,
		TTL:          invocation.TTL,
		JSON:         invocation.JSON,
		Now:          application.now(),
	}, func(ctx context.Context, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
		return application.renew(ctx, repository, assignmentID, expiresAt)
	})
	if err != nil {
		return 1, err
	}
	if _, err := io.WriteString(application.stdout, result.Output); err != nil {
		return 1, fmt.Errorf("write renew result: %w", err)
	}
	return 0, nil
}

// runRemove performs remove, which has no success output.
func (application *Application) runRemove(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if application.remove == nil {
		return 1, errors.New("remove command is not configured")
	}
	if err := application.remove(ctx, RemoveInput{
		Repository: repository,
		CWD:        application.cwd,
		Path:       invocation.Path,
		Force:      invocation.Force,
	}); err != nil {
		return 1, err
	}
	return 0, nil
}

// runWarm executes pool warm-up after validation.
func (application *Application) runWarm(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if application.warm == nil {
		return 1, errors.New("warm command is not configured")
	}
	result, err := Warm(ctx, WarmInput{
		Count: invocation.Count,
		From:  invocation.From,
		Fetch: invocation.Fetch,
		JSON:  invocation.JSON,
	}, func(ctx context.Context, request WarmRequest) (lifecycle.WarmResult, error) {
		return application.warm(ctx, repository, request)
	})
	if err != nil {
		return 1, err
	}
	if _, err := io.WriteString(application.stdout, result.Output); err != nil {
		return 1, fmt.Errorf("write warm result: %w", err)
	}
	return 0, nil
}

// runGC executes garbage collection after validation.
func (application *Application) runGC(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if application.gc == nil {
		return 1, errors.New("gc command is not configured")
	}
	result, err := GC(ctx, GCInput{
		MaxAgeMinutes:        invocation.MaxAge,
		Apply:                invocation.Apply,
		ForceExpired:         invocation.ForceExpired,
		JSON:                 invocation.JSON,
		CurrentWorkspacePath: application.cwd,
		Now:                  application.now(),
	}, func(ctx context.Context, request GCRequest) (lifecycle.GCResult, error) {
		return application.gc(ctx, repository, request)
	})
	if err != nil {
		return 1, err
	}
	if _, err := io.WriteString(application.stdout, result.Output); err != nil {
		return 1, fmt.Errorf("write gc result: %w", err)
	}
	return 0, nil
}
