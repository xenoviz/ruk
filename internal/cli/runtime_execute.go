package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/ports"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

func runtimeRun(ctx context.Context, input RunRouteInput, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions) (int, error) {
	if err := validateRepositoryContext(input.Repository); err != nil {
		return 1, err
	}
	if input.Repository.PrimaryCheckout && !input.AllowSharedCheckout {
		cfg, err := config.Load(input.Repository.Root)
		if err != nil {
			return 1, err
		}
		run := func() (int, error) {
			if guardErr := defaultSharedCheckoutGuard(ctx, input.Repository, cfg); guardErr != nil {
				var warning *SharedCheckoutWarning
				if !errors.As(guardErr, &warning) {
					return 1, guardErr
				}
				if input.Stderr != nil {
					if _, writeErr := fmt.Fprintln(input.Stderr, warning.Error()); writeErr != nil {
						return 1, fmt.Errorf("write shared-checkout warning: %w", writeErr)
					}
				}
			}
			return runtimeExecute(ctx, input.Repository, input.CWD, input.Command, false, input.AllowSharedCheckout, "", now, newID, syncRoute, options)
		}
		if cfg.SharedCheckoutPolicy == config.Warn || cfg.SharedCheckoutPolicy == config.Allow {
			return run()
		}
		fence := options.PrimaryCheckoutFence
		if fence == nil {
			fence = defaultPrimaryCheckoutFence
		}
		var code int
		var runErr error
		fenceErr := fence(ctx, input.Repository, func() error {
			code, runErr = run()
			return runErr
		})
		if fenceErr != nil {
			return code, fenceErr
		}
		return code, runErr
	}
	return runtimeExecute(ctx, input.Repository, input.CWD, input.Command, false, input.AllowSharedCheckout, "", now, newID, syncRoute, options)
}

func runtimeExec(ctx context.Context, input ExecRouteInput, now func() time.Time, newID func() string, mutations MutationAdapters, options RuntimeDefaultsOptions) (int, error) {
	if mutations.Acquire == nil {
		return 1, errors.New("acquire command is not configured")
	}
	acquired, err := mutations.Acquire(ctx, input.Repository, input.Acquire)
	if err != nil {
		return 1, err
	}
	if acquired.AssignmentID == "" || acquired.Path == "" {
		return 1, errors.New("acquire returned an incomplete assignment")
	}
	code, err, expiresAt, ownership := runtimeExecuteWithExpiry(ctx, input.Repository, input.CWD, input.Command, true, input.AllowSharedCheckout, acquired.AssignmentID, now, newID, mutations.Sync, options, acquired.Path)
	if err != nil {
		return code, runtimeExecutionError(acquired, expiresAt, ownership, err)
	}
	return code, nil
}

func runtimeExecute(ctx context.Context, repository git.Repository, cwd string, command []string, execMode, allowShared bool, assignmentID string, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions, paths ...string) (int, error) {
	code, err, _, _ := runtimeExecuteWithExpiry(ctx, repository, cwd, command, execMode, allowShared, assignmentID, now, newID, syncRoute, options, paths...)
	return code, err
}

// runtimeExecuteWithExpiry returns the latest durable assignment expiry along
// with the execution result. Activity keepers may renew the assignment while
// the child is running, so retained errors must use that post-operation value
// instead of the expiry returned by the initial acquire.
func runtimeExecuteWithExpiry(ctx context.Context, repository git.Repository, cwd string, command []string, execMode, allowShared bool, assignmentID string, now func() time.Time, newID func() string, syncRoute SyncRouteOperation, options RuntimeDefaultsOptions, paths ...string) (int, error, string, runtimeExecutionOwnership) {
	if strings.TrimSpace(cwd) == "" {
		return 1, errors.New("execution working directory must not be empty"), "", runtimeExecutionOwnershipUnknown
	}
	store, locker, service, err := runtimeState(ctx, repository, now, newID)
	if err != nil {
		return 1, err, "", runtimeExecutionOwnershipUnknown
	}
	workspacePath := repository.Root
	if len(paths) > 0 && paths[0] != "" {
		workspacePath = paths[0]
	}
	if assignmentID == "" {
		snapshot, readErr := store.Read(ctx)
		if readErr != nil {
			return 1, readErr, "", runtimeExecutionOwnershipUnknown
		}
		key, keyErr := state.TreeKey(workspacePath)
		if keyErr != nil {
			return 1, keyErr, "", runtimeExecutionOwnershipUnknown
		}
		workspace, managed := snapshot.Workspaces[key]
		if !managed {
			if syncRoute == nil {
				return 1, errors.New("sync command is not configured"), "", runtimeExecutionOwnershipUnknown
			}
			repo := repository
			repo.Root = workspacePath
			if _, syncErr := syncRoute(ctx, SyncCommandInput{Repository: repo, GuardSharedCheckout: false, AllowSharedCheckout: allowShared, Emit: false}); syncErr != nil {
				return 1, syncErr, "", runtimeExecutionOwnershipUnknown
			}
			current, currentErr := store.Read(ctx)
			if currentErr != nil {
				return 1, currentErr, "", runtimeExecutionOwnershipUnknown
			}
			if _, becameManaged := current.Workspaces[key]; becameManaged {
				return 1, fmt.Errorf("Workspace %s became managed during dependency synchronization", workspacePath), "", runtimeExecutionOwnershipUnknown
			}
			runner := options.ExecuteRunner
			if runner.Spawner == nil {
				runner = processpkg.NewRunner()
			}
			result, runErr := runner.Run(ctx, command, processpkg.RunOptions{
				Dir: workspacePath, Env: os.Environ(), Mode: processpkg.Attached,
				Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
			})
			return result.ExitCode, runErr, "", runtimeExecutionOwnershipUnknown
		}
		if workspace.Assignment == nil || workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil {
			return 1, fmt.Errorf("Workspace %s is not assigned", workspacePath), "", runtimeExecutionOwnershipUnknown
		}
		assignmentID = workspace.Assignment.ID
	}
	if syncRoute == nil {
		return 1, errors.New("sync command is not configured"), "", runtimeExecutionOwnershipUnknown
	}
	baseRepository := repository
	baseRepository.Root = workspacePath
	if execMode {
		baseRepository.PrimaryCheckout = false
	}
	synchronize := func(ctx context.Context, expectedID, path string) error {
		repo := baseRepository
		repo.Root = path
		ensure := dependencies.EnsureInput{BeforePrepare: func() error { return verifyRuntimeAssignment(ctx, store, expectedID, path) }}
		result, err := syncRoute(ctx, SyncCommandInput{Repository: repo, Ensure: ensure, GuardSharedCheckout: false, AllowSharedCheckout: allowShared, Emit: false})
		_ = result
		return err
	}
	release := func(ctx context.Context, id string) error {
		operation, releaseErr := runtimeReleaseService(repository, store, locker, service, options)
		if releaseErr != nil {
			return releaseErr
		}
		_, releaseErr = operation.ReleaseAssignment(ctx, id, lifecycle.ReleaseOptions{})
		return releaseErr
	}
	activity := options.ExecuteActivity
	if activity == nil {
		activity = NewActivityRunner(ActivityRunnerOptions{Lifecycle: service, Reader: store, Now: now, NewID: newID}).ExecuteActivityRunner()
	}
	runner := options.ExecuteRunner
	if runner.Spawner == nil {
		runner = processpkg.NewRunner()
	}
	if runner.Forwarder == nil {
		runner.Forwarder = processpkg.NewNativeSignalForwarder()
	}
	execute := NewExecuteService(ExecuteOptions{
		Lifecycle: service, Reader: store, Runner: runner, Synchronize: synchronize,
		Activity: activity, Release: release, HandoffLocker: locker,
		HandoffPath: func(path string) (string, error) {
			return MutationWorkspaceLockPath(repository.CommonDir, path)
		},
	})
	environment := os.Environ()
	snapshot, err := store.Read(ctx)
	if err != nil {
		return 1, err, "", runtimeExecutionOwnershipUnknown
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return 1, err, "", runtimeExecutionOwnershipUnknown
	}
	if workspace, ok := snapshot.Workspaces[key]; ok && workspace.Assignment != nil {
		additions, envErr := ports.BuildEnvironment(workspace.Assignment.Ports)
		if envErr != nil {
			return 1, envErr, "", runtimeExecutionOwnershipUnknown
		}
		for name, value := range additions {
			environment = append(environment, name+"="+value)
		}
	}
	signalSource := options.ExecuteSignals
	if signalSource == nil {
		signalSource = runtimeManagedSignals
	}
	signals, stopSignals := signalSource()
	if stopSignals == nil {
		stopSignals = func() {}
	}
	defer stopSignals()
	result, err := execute.Execute(ctx, ExecuteInput{AssignmentID: assignmentID, WorkspacePath: workspacePath, Command: command, Env: environment, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Mode: processpkg.Detached, Exec: execMode, Signals: signals})
	expiresAt := ""
	ownership := runtimeExecutionOwnershipUnknown
	if assignmentID != "" {
		if current, readErr := store.Read(context.WithoutCancel(ctx)); readErr == nil {
			ownership = runtimeExecutionOwnershipReleased
			if currentWorkspace, ok := current.Workspaces[key]; ok && currentWorkspace.Assignment != nil && currentWorkspace.Assignment.ID == assignmentID {
				expiresAt = currentWorkspace.Assignment.ExpiresAt
				ownership = runtimeExecutionOwnershipRetained
			}
		}
		if result.Released {
			ownership = runtimeExecutionOwnershipReleased
		}
	}
	return result.ExitCode, err, expiresAt, ownership
}

func retainedRuntimeExecutionError(acquired AcquireResult, expiresAt string, err error) error {
	if expiresAt == "" {
		expiresAt = acquired.ExpiresAt
	}
	if retained := RetainedAssignmentFailure(acquired.AssignmentID, acquired.Path, expiresAt, err); retained != nil {
		return retained
	}
	return err
}

func runtimeExecutionError(acquired AcquireResult, expiresAt string, ownership runtimeExecutionOwnership, err error) error {
	if ownership == runtimeExecutionOwnershipReleased {
		return err
	}
	return retainedRuntimeExecutionError(acquired, expiresAt, err)
}
