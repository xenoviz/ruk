package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

func runtimeShell(ctx context.Context, input ShellRouteInput, now func() time.Time, newID func() string, mutations MutationAdapters, options RuntimeDefaultsOptions) (ShellResult, error) {
	if err := validateRepositoryContext(input.Repository); err != nil {
		return ShellResult{}, err
	}
	if mutations.Acquire == nil || mutations.Release == nil {
		return ShellResult{}, errors.New("shell lifecycle operations are not configured")
	}
	var store *state.Store
	var locker *lock.DirectoryLocker
	var lifecycleService *lifecycle.Service
	// Fully injected shell seams do not need native state or process identity.
	// Keep constructing the production state bundle whenever either the native
	// terminal or the native activity runner is still required.
	if options.ShellTerminal == nil || options.ExecuteActivity == nil {
		var err error
		store, locker, lifecycleService, err = runtimeState(ctx, input.Repository, now, newID)
		if err != nil {
			return ShellResult{}, err
		}
	}
	terminal := options.ShellTerminal
	stopShellSignals := func() {}
	if terminal == nil {
		shellSignals, stopSignals := runtimeManagedSignals()
		stopShellSignals = stopSignals
		terminal = NewNativeShellTerminal(ShellTerminalOptions{
			HandoffLocker: locker,
			HandoffPath: func(path string) (string, error) {
				return MutationWorkspaceLockPath(input.Repository.CommonDir, path)
			},
			Validate: func(ctx context.Context, assignmentID, path string) error {
				return verifyRuntimeAssignment(ctx, store, assignmentID, path)
			},
			Register: func(ctx context.Context, assignmentID string, record state.TrackedProcessRecord) error {
				_, err := lifecycleService.AddAssignmentProcess(ctx, assignmentID, record)
				return err
			},
			Remove: func(ctx context.Context, assignmentID string, record state.TrackedProcessRecord) error {
				_, err := lifecycleService.RemoveAssignmentProcess(ctx, assignmentID, record.PID, record.StartedAt)
				return err
			},
			Signals: shellSignals,
		})
	}
	defer stopShellSignals()
	activity := options.ExecuteActivity
	if activity == nil {
		activity = NewActivityRunner(ActivityRunnerOptions{Lifecycle: lifecycleService, Reader: store, Now: now, NewID: newID}).ExecuteActivityRunner()
	}
	service := NewShellService(ShellOptions{
		Acquire: func(ctx context.Context, request AcquireInput) (AcquireResult, error) {
			return mutations.Acquire(ctx, input.Repository, request)
		},
		Terminal: terminal,
		Activity: activity,
		Expiry: func(ctx context.Context, assignmentID, path string) (string, bool) {
			snapshot, err := store.Read(ctx)
			if err != nil || snapshot == nil {
				return "", false
			}
			key, err := state.TreeKey(path)
			if err != nil {
				return "", false
			}
			workspace, ok := snapshot.Workspaces[key]
			if !ok || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
				return "", false
			}
			return workspace.Assignment.ExpiresAt, true
		},
		Release: func(ctx context.Context, assignmentID string) error {
			_, err := mutations.Release(ctx, ReleaseInput{Repository: input.Repository, AssignmentID: assignmentID})
			return err
		},
	})
	result, err := service.Shell(ctx, ShellInput{
		Branch: input.Branch, From: input.From, Fetch: input.Fetch, TTL: input.TTL,
		Owner: input.Owner, Ports: append([]string(nil), input.Ports...), Now: input.Now,
		Environment: runtimeEnvironmentMap(os.Environ()),
		Stdin:       input.Stdin, Stdout: input.Stdout, Stderr: input.Stderr,
	})
	if err != nil {
		return result, err
	}
	if input.Stderr != nil {
		if _, err := fmt.Fprintf(input.Stderr, "Released %s\n", result.Path); err != nil {
			return result, fmt.Errorf("write shell release: %w", err)
		}
	}
	return result, nil
}
