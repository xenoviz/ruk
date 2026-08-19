package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

// ExecuteStateReader is the read-only state seam used to validate an
// assignment before and after dependency synchronization.
type ExecuteStateReader interface {
	Read(context.Context) (*state.State, error)
}

// ExecuteDependencySynchronizer refreshes one workspace's dependencies. The
// service rechecks the assignment fence after this callback returns.
type ExecuteDependencySynchronizer func(context.Context, string, string) error

// ExecuteActivityRunner surrounds the complete managed operation, including
// dependency synchronization, process wait, and process-record removal. A
// production adapter can implement keeper heartbeats and renewal here.
type ExecuteActivityRunner func(context.Context, string, func(context.Context) error) error

// ExecuteRelease releases an assignment after an exec command has finished
// and its tracked process record has been removed.
type ExecuteRelease func(context.Context, string) error

// ExecuteProcesser proves whether a detached process tree still belongs to
// the exact durable identity recorded for the assignment.
type ExecuteProcesser interface {
	Exists(context.Context, state.TrackedProcessRecord) (bool, error)
}

// ExecuteHandoffLocker exposes one native directory-lock guard. The guard is
// released by the process runner immediately after registration (or failed
// registration cleanup), before a long-lived child is waited on.
type ExecuteHandoffLocker interface {
	Acquire(context.Context, string) (*lock.Guard, error)
}

// ExecuteOptions contains the injected seams for managed execution.
type ExecuteOptions struct {
	Lifecycle     *lifecycle.Service
	Reader        ExecuteStateReader
	Runner        processpkg.Runner
	Processes     ExecuteProcesser
	Synchronize   ExecuteDependencySynchronizer
	Activity      ExecuteActivityRunner
	Release       ExecuteRelease
	HandoffLocker ExecuteHandoffLocker
	HandoffPath   func(string) (string, error)
}

// ExecuteService runs one command in a fenced managed workspace.
type ExecuteService struct {
	lifecycle        *lifecycle.Service
	reader           ExecuteStateReader
	runner           processpkg.Runner
	processes        ExecuteProcesser
	synchronize      ExecuteDependencySynchronizer
	activity         ExecuteActivityRunner
	releaseOperation ExecuteRelease
	handoffLocker    ExecuteHandoffLocker
	handoffPath      func(string) (string, error)
}

const executeRecoveryTimeout = 30 * time.Second

// NewExecuteService constructs an execution service. OS/process behavior
// remains behind process.Runner and the injected activity/release seams.
func NewExecuteService(options ExecuteOptions) *ExecuteService {
	if options.Lifecycle == nil {
		panic("cli: nil execute lifecycle")
	}
	if options.Runner.Spawner == nil {
		options.Runner = processpkg.NewRunner()
	}
	if options.Processes == nil {
		manager := processpkg.NewNativeProcessManager()
		options.Processes = manager
	}
	return &ExecuteService{
		lifecycle:        options.Lifecycle,
		reader:           options.Reader,
		runner:           options.Runner,
		processes:        options.Processes,
		synchronize:      options.Synchronize,
		activity:         options.Activity,
		releaseOperation: options.Release,
		handoffLocker:    options.HandoffLocker,
		handoffPath:      options.HandoffPath,
	}
}

// ExecuteInput describes a managed run or exec operation. Exec requests
// release the assignment after the child exits and its tracked record drains.
type ExecuteInput struct {
	AssignmentID  string
	WorkspacePath string
	Command       []string
	Env           []string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	CaptureLimit  int
	Mode          processpkg.ProcessMode
	Exec          bool
	Signals       <-chan os.Signal
}

// ExecuteResult contains the native process result and whether exec released
// the assignment after process tracking completed.
type ExecuteResult struct {
	processpkg.RunResult
	AssignmentID string
	Released     bool
}

// Execute validates ownership, synchronizes dependencies, tracks the child,
// forwards detached signals, and waits for process completion. Exec releases
// only after the exact tracked process record has been removed.
func (service *ExecuteService) Execute(ctx context.Context, input ExecuteInput) (result ExecuteResult, err error) {
	if service == nil || service.lifecycle == nil {
		return result, errors.New("execute service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if input.AssignmentID == "" {
		return result, errors.New("assignment ID must not be empty")
	}
	if input.WorkspacePath == "" {
		return result, errors.New("workspace path must not be empty")
	}
	if len(input.Command) == 0 || input.Command[0] == "" {
		return result, errors.New("command must not be empty")
	}
	if input.Mode != processpkg.Attached && input.Mode != processpkg.Detached {
		return result, errors.New("unsupported process mode")
	}
	if service.reader == nil {
		return result, errors.New("execute state reader is not configured")
	}
	if service.synchronize == nil {
		return result, errors.New("dependency synchronization is not configured")
	}
	if input.Exec && service.releaseOperation == nil {
		return result, errors.New("execute release is not configured")
	}

	if err := service.validateAssignment(ctx, input.AssignmentID, input.WorkspacePath); err != nil {
		return result, service.releaseBeforeChildFailure(ctx, input, &result, err)
	}
	operation := func(operationCtx context.Context) error {
		if err := service.synchronize(operationCtx, input.AssignmentID, input.WorkspacePath); err != nil {
			return err
		}
		if err := service.validateAssignment(operationCtx, input.AssignmentID, input.WorkspacePath); err != nil {
			return err
		}
		return service.runTracked(operationCtx, input, &result)
	}
	if service.activity != nil {
		err = service.activity(ctx, input.AssignmentID, operation)
	} else {
		err = operation(ctx)
	}
	if err != nil && input.Exec && !result.RunResult.Started {
		err = service.releaseBeforeChildFailure(ctx, input, &result, err)
	}
	return result, err
}

func (service *ExecuteService) releaseBeforeChildFailure(ctx context.Context, input ExecuteInput, result *ExecuteResult, cause error) error {
	if !input.Exec || service.releaseOperation == nil || input.AssignmentID == "" {
		return cause
	}
	if err := service.release(ctx, input.AssignmentID); err != nil {
		return errors.Join(cause, fmt.Errorf("exec setup failed; Assignment %s retained for recovery: %w", input.AssignmentID, err))
	}
	if result != nil {
		result.Released = true
	}
	return cause
}

func (service *ExecuteService) validateAssignment(ctx context.Context, assignmentID, workspacePath string) error {
	current, err := service.reader.Read(ctx)
	if err != nil {
		return err
	}
	if current == nil {
		return errors.New("execute state reader returned nil state")
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return err
	}
	workspace, exists := current.Workspaces[key]
	if !exists || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
		return fmt.Errorf("Assignment %s does not exist or no longer owns %s", assignmentID, workspacePath)
	}
	if workspace.Lifecycle != state.LifecycleAssigned {
		return fmt.Errorf("Workspace %s is %s, expected assigned", workspace.Path, workspace.Lifecycle)
	}
	if workspace.OperationID != nil {
		return fmt.Errorf("Assignment %s acquisition is still in progress", assignmentID)
	}
	return nil
}

func (service *ExecuteService) runTracked(ctx context.Context, input ExecuteInput, result *ExecuteResult) error {
	var handoffGuard *lock.Guard
	if service.handoffLocker != nil {
		if service.handoffPath == nil {
			return errors.New("execute workspace handoff lock path is not configured")
		}
		lockPath, err := service.handoffPath(input.WorkspacePath)
		if err != nil {
			return err
		}
		handoffGuard, err = service.handoffLocker.Acquire(ctx, lockPath)
		if err != nil {
			return err
		}
	}
	var handoffMu sync.Mutex
	var handoffErr error
	releaseHandoff := func() {
		handoffMu.Lock()
		defer handoffMu.Unlock()
		if handoffGuard == nil {
			return
		}
		if err := handoffGuard.Release(); err != nil {
			handoffErr = errors.Join(handoffErr, err)
			return
		}
		handoffGuard = nil
	}
	defer func() {
		if handoffGuard != nil {
			if err := handoffGuard.Release(); err != nil {
				handoffMu.Lock()
				handoffErr = errors.Join(handoffErr, err)
				handoffMu.Unlock()
			}
		}
	}()
	if handoffGuard != nil {
		if err := service.validateAssignment(ctx, input.AssignmentID, input.WorkspacePath); err != nil {
			return err
		}
	}
	var mu sync.Mutex
	var tracked *state.TrackedProcessRecord
	var pending os.Signal
	var signalErr error
	watchCtx, stopWatching := context.WithCancel(ctx)
	defer stopWatching()
	var watcher sync.WaitGroup
	if input.Mode == processpkg.Detached && input.Signals != nil {
		for {
			select {
			case signal, ok := <-input.Signals:
				if !ok {
					input.Signals = nil
					break
				}
				if isForwardedSignal(signal) {
					pending = signal
				}
				continue
			default:
			}
			break
		}
	}
	if input.Mode == processpkg.Detached && input.Signals != nil {
		watcher.Add(1)
		go func() {
			defer watcher.Done()
			for {
				select {
				case signal, ok := <-input.Signals:
					if !ok {
						return
					}
					if !isForwardedSignal(signal) {
						continue
					}
					mu.Lock()
					if tracked == nil {
						pending = signal
						mu.Unlock()
						continue
					}
					forwardErr := service.runner.ForwardSignal(ctx, *tracked, signal)
					if forwardErr != nil && signalErr == nil {
						signalErr = forwardErr
					}
					mu.Unlock()
				case <-watchCtx.Done():
					return
				}
			}
		}()
	}
	runResult, runErr := service.runner.Run(ctx, input.Command, processpkg.RunOptions{
		Dir: input.WorkspacePath, Env: input.Env, Mode: input.Mode,
		Stdin: input.Stdin, Stdout: input.Stdout, Stderr: input.Stderr,
		CaptureLimit:          input.CaptureLimit,
		SuperviseCancellation: true,
		HandoffComplete:       releaseHandoff,
		Register: func(registerCtx context.Context, record state.TrackedProcessRecord) error {
			if err := service.validateAssignment(registerCtx, input.AssignmentID, input.WorkspacePath); err != nil {
				return err
			}
			if _, err := service.lifecycle.AddAssignmentProcess(registerCtx, input.AssignmentID, record); err != nil {
				return err
			}
			mu.Lock()
			copy := record
			tracked = &copy
			queued := pending
			pending = nil
			mu.Unlock()
			if queued != nil {
				if err := service.runner.ForwardSignal(registerCtx, record, queued); err != nil {
					mu.Lock()
					if signalErr == nil {
						signalErr = err
					}
					mu.Unlock()
				}
			}
			return nil
		},
	})
	// Test runners and future adapters may not invoke the handoff callback;
	// release here as a safe fallback after Run returns.
	releaseHandoff()
	stopWatching()
	watcher.Wait()
	mu.Lock()
	finalSignalErr := signalErr
	trackedRecord := tracked
	mu.Unlock()
	postRunContext := ctx
	cancelPostRun := func() {}
	if ctx.Err() != nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		postRunContext, cancelPostRun = executeRecoveryContext(ctx)
	}
	defer cancelPostRun()
	recordRemoved := trackedRecord == nil
	if trackedRecord != nil {
		drained := true
		if input.Mode == processpkg.Detached {
			if service.processes == nil {
				runErr = errors.Join(runErr, errors.New("verify detached process cleanup: process manager is unavailable"))
				drained = false
			} else {
				alive, existsErr := service.processes.Exists(postRunContext, *trackedRecord)
				if existsErr != nil {
					runErr = errors.Join(runErr, fmt.Errorf("verify detached process %d cleanup: %w", trackedRecord.PID, existsErr))
					drained = false
				} else if alive {
					runErr = errors.Join(runErr, fmt.Errorf("detached process tree %d remains after its leader exited", trackedRecord.PID))
					drained = false
				}
			}
		}
		if drained {
			if _, removeErr := service.lifecycle.RemoveAssignmentProcess(postRunContext, input.AssignmentID, trackedRecord.PID, trackedRecord.StartedAt); removeErr != nil {
				runErr = errors.Join(runErr, removeErr)
			} else {
				recordRemoved = true
			}
		}
	}
	if finalSignalErr != nil {
		runErr = errors.Join(runErr, finalSignalErr)
	}
	result.RunResult = runResult
	result.AssignmentID = input.AssignmentID
	handoffMu.Lock()
	finalHandoffErr := handoffErr
	handoffMu.Unlock()
	runErr = errors.Join(runErr, finalHandoffErr)
	if finalHandoffErr != nil {
		// The workspace handoff fence could not be released cleanly. Keep the
		// assignment for explicit recovery instead of racing a recycle.
		return runErr
	}
	if input.Exec && recordRemoved && processCleanupSafe(runErr) {
		if releaseErr := service.release(postRunContext, input.AssignmentID); releaseErr != nil {
			return errors.Join(runErr, releaseErr)
		}
		result.Released = true
	}
	return runErr
}

func executeRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), executeRecoveryTimeout)
}

func isForwardedSignal(signal os.Signal) bool {
	return signal == os.Interrupt || signal == syscall.SIGTERM
}

func (service *ExecuteService) release(ctx context.Context, assignmentID string) error {
	if service.releaseOperation == nil {
		return errors.New("execute release is not configured")
	}
	return service.releaseOperation(ctx, assignmentID)
}

func processCleanupSafe(err error) bool {
	if err == nil {
		return true
	}
	var waitErr *processpkg.ProcessWaitError
	if errors.As(err, &waitErr) {
		return false
	}
	var registration *processpkg.RegistrationError
	if errors.As(err, &registration) {
		return registration.Cleanup == nil
	}
	var setup *processpkg.ProcessSetupError
	if errors.As(err, &setup) {
		return setup.Cleanup == nil
	}
	return true
}
