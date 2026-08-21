package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/xenoviz/ruk/internal/state"
)

// Runner composes native process primitives while keeping OS execution
// behind Spawner, ProcessDescriber, and ProcessCleaner boundaries.
type Runner struct {
	Spawner   Spawner
	Describer ProcessDescriber
	Cleaner   ProcessCleaner
	Forwarder SignalForwarder
}

// NewRunner returns a native runner. Windows children are contained by a
// kill-on-close Job Object, while POSIX detached children use an identity-
// fenced process group.
func NewRunner() Runner {
	return Runner{
		Spawner:   OSProcessSpawner{},
		Describer: NativeProcessDescriber{Probe: Inspector{}, Table: NativeTable{}},
		Cleaner:   NativeProcessCleaner{Probe: Inspector{}, Signaler: defaultGroupSignaler()},
	}
}

// Run starts, identity-describes, and (when supplied) registers one command.
// The registration callback completes before Run waits for or returns the
// child, which closes the registration-to-handoff race.
func (runner Runner) Run(ctx context.Context, command []string, options RunOptions) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return RunResult{}, errors.New("process: command must not be empty")
	}
	if options.Mode != Attached && options.Mode != Detached {
		return RunResult{}, errors.New("process: unsupported process mode")
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}
	if runner.Spawner == nil || runner.Describer == nil {
		return RunResult{}, errors.New("process: spawn dependencies are unavailable")
	}
	limit := options.CaptureLimit
	if limit == 0 {
		limit = 4096
	}
	if limit < 0 {
		return RunResult{}, errors.New("process: capture limit must not be negative")
	}
	stdout := NewTailBuffer(limit)
	stderr := NewTailBuffer(limit)
	operationCtx := ctx
	if options.SuperviseCancellation {
		operationCtx = context.WithoutCancel(ctx)
	}
	request := SpawnRequest{
		Command: command[0], Args: append([]string(nil), command[1:]...),
		Dir: options.Dir, Env: append([]string(nil), options.Env...), Mode: options.Mode,
		ForegroundTerminal: options.ForegroundTerminal,
		Stdin:              options.Stdin,
		Stdout:             outputWriter(stdout, options.Stdout),
		Stderr:             outputWriter(stderr, options.Stderr),
	}
	child, err := runner.Spawner.Spawn(operationCtx, request)
	if err != nil {
		return RunResult{}, err
	}
	if child == nil {
		return RunResult{}, errors.New("process: spawner returned an invalid child")
	}
	pid := child.PID()
	if pid <= 0 {
		cause := errors.New("process: spawner returned a child with an invalid PID")
		cleanupErr := runner.cleanupUnknownChild(ctx, child, options.Mode, state.TrackedProcessRecord{}, cause, nil)
		if options.HandoffComplete != nil {
			options.HandoffComplete()
		}
		return RunResult{Started: true}, &ProcessSetupError{Cause: cause, Cleanup: cleanupErr}
	}
	identityCtx, cancelIdentity := boundedCleanupContext(ctx)
	record, err := runner.Describer.Describe(identityCtx, pid, options.Mode, command)
	cancelIdentity()
	if err != nil {
		// A describer error makes any returned record untrusted. Do not use a
		// partial identity to signal a possibly reused PID.
		return runner.postSpawnFailure(operationCtx, child, options.Mode, state.TrackedProcessRecord{}, err, stdout, stderr, options.Register, command, options.HandoffComplete)
	}
	if err := validateRunnerRecord(record, pid, options.Mode); err != nil {
		return runner.postSpawnFailure(operationCtx, child, options.Mode, state.TrackedProcessRecord{}, err, stdout, stderr, options.Register, command, options.HandoffComplete)
	}
	if options.Register != nil {
		registerCtx, cancelRegister := boundedCleanupContext(ctx)
		registerErr := options.Register(registerCtx, record)
		cancelRegister()
		if registerErr != nil {
			cleanupErr := error(nil)
			if runner.Cleaner == nil {
				cleanupErr = errors.New("process: registration cleanup is unavailable")
			} else {
				cleanupCtx, cancelCleanup := boundedCleanupContext(ctx)
				defer cancelCleanup()
				cleanupErr = runner.Cleaner.Cleanup(cleanupCtx, child, record)
			}
			if cleanupErr == nil {
				// Successful cleanup must reap the child before this handoff fails.
				status, waitErr := waitChildAfterCleanup(ctx, child)
				cleanupErr = waitErr
				if cleanupErr == nil {
					cleanupErr = errors.Join(waitStatusError(status), status.BoundaryError)
				}
				if cleanupErr != nil {
					cleanupErr = cancellationSafetyError(child, record, options.Mode, cleanupErr)
				}
				if cleanupErr == nil && options.Mode == Detached {
					verifier, ok := runner.Cleaner.(ProcessCleanupVerifier)
					if !ok {
						cleanupErr = cancellationSafetyError(child, record, options.Mode, errors.New("process: detached registration cleanup cannot verify descendants drained"))
					} else {
						verifyCtx, cancelVerify := boundedCleanupContext(ctx)
						alive, verifyErr := verifier.Exists(verifyCtx, record)
						cancelVerify()
						if verifyErr != nil {
							cleanupErr = cancellationSafetyError(child, record, options.Mode, fmt.Errorf("process: verify detached registration cleanup: %w", verifyErr))
						} else if alive {
							cleanupErr = cancellationSafetyError(child, record, options.Mode, errors.New("process: detached registration cleanup left descendants running"))
						}
					}
				}
			} else {
				retainAfterCleanupFailure(child)
				cleanupErr = cancellationSafetyError(child, record, options.Mode, cleanupErr)
			}
			if options.HandoffComplete != nil {
				options.HandoffComplete()
			}
			return RunResult{PID: child.PID(), Started: true, Record: record, Stdout: stdout.String(), Stderr: stderr.String()}, &RegistrationError{Register: registerErr, Cleanup: cleanupErr}
		}
		if options.HandoffComplete != nil {
			options.HandoffComplete()
		}
	} else if options.HandoffComplete != nil {
		// There is no durable registration callback to define the handoff.
		options.HandoffComplete()
	}
	status, supervisionErr := runner.waitSupervised(ctx, child, record, options)
	result := RunResult{
		PID: child.PID(), Started: true, ExitCode: normalizeExitCode(status), Signal: status.Signal,
		Stdout: stdout.String(), Stderr: stderr.String(), Record: record,
	}
	if supervisionErr != nil {
		return result, supervisionErr
	}
	if waitErr := waitStatusError(status); waitErr != nil || status.BoundaryError != nil {
		cause := errors.Join(waitErr, status.BoundaryError)
		return result, cancellationSafetyError(child, record, options.Mode, cause)
	}
	return result, nil
}

func (runner Runner) waitSupervised(ctx context.Context, child Child, record state.TrackedProcessRecord, options RunOptions) (ExitStatus, error) {
	if !options.SuperviseCancellation {
		return child.Wait(), nil
	}

	waitResult := make(chan ExitStatus, 1)
	go func() { waitResult <- child.Wait() }()
	select {
	case status := <-waitResult:
		verifyCtx, cancelVerify := boundedCleanupContext(ctx)
		verifyErr := runner.verifyDetachedTree(verifyCtx, record, options)
		cancelVerify()
		if verifyErr != nil {
			verifyErr = errors.Join(verifyErr, waitStatusError(status), status.BoundaryError)
			safetyErr := cancellationSafetyError(child, record, options.Mode, verifyErr)
			if ctx.Err() != nil {
				return status, errors.Join(ctx.Err(), safetyErr)
			}
			return status, safetyErr
		}
		if ctx.Err() != nil {
			if waitErr := errors.Join(waitStatusError(status), status.BoundaryError); waitErr != nil {
				return status, errors.Join(ctx.Err(), cancellationSafetyError(child, record, options.Mode, waitErr))
			}
			return status, ctx.Err()
		}
		return status, nil
	case <-ctx.Done():
		cleanupCtx, cancelCleanup := boundedCleanupContext(ctx)
		cleanupErr := runner.cleanupCancelledChild(cleanupCtx, child, record, options.Mode)
		cancelCleanup()
		if cleanupErr != nil {
			// Do not block the caller when the native boundary cannot be proven
			// safe. The owning workspace must remain retained for recovery.
			return ExitStatus{}, errors.Join(ctx.Err(), cancellationSafetyError(child, record, options.Mode, cleanupErr))
		}
		waitCtx, cancelWait := boundedCleanupContext(ctx)
		var status ExitStatus
		select {
		case status = <-waitResult:
			cancelWait()
		case <-waitCtx.Done():
			cancelWait()
			return ExitStatus{}, errors.Join(ctx.Err(), cancellationSafetyError(child, record, options.Mode, fmt.Errorf("process: wait after cancellation cleanup did not complete: %w", waitCtx.Err())))
		}
		verifyCtx, cancelVerify := boundedCleanupContext(ctx)
		verifyErr := runner.verifyDetachedTree(verifyCtx, record, options)
		cancelVerify()
		waitErr := errors.Join(waitStatusError(status), status.BoundaryError)
		if verifyErr != nil || waitErr != nil {
			verifyCause := error(nil)
			if verifyErr != nil {
				verifyCause = fmt.Errorf("process: verify cancellation cleanup: %w", verifyErr)
			}
			return status, errors.Join(ctx.Err(), cancellationSafetyError(child, record, options.Mode, errors.Join(verifyCause, waitErr)))
		}
		return status, ctx.Err()
	}
}

// ForwardSignal invokes the configured forwarding hook for a registered
// record. Callers can invoke it from an os/signal subscription they own.
func (runner Runner) ForwardSignal(ctx context.Context, record state.TrackedProcessRecord, signal os.Signal) error {
	if runner.Forwarder == nil {
		return errors.New("process: signal forwarder is unavailable")
	}
	return runner.Forwarder.Forward(ctx, record, signal)
}
