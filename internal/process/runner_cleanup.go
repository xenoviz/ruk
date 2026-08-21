package process

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

const processCleanupTimeout = 30 * time.Second

func boundedCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), processCleanupTimeout)
}

// waitChildAfterCleanup gives a child a bounded window to report its final
// status after the native boundary has been cleaned. The wait goroutine owns
// the eventual reap when the window expires; callers must retain the owning
// assignment because safety could not be proven in time.
func waitChildAfterCleanup(ctx context.Context, child Child) (ExitStatus, error) {
	waitResult := make(chan ExitStatus, 1)
	go func() { waitResult <- child.Wait() }()
	waitCtx, cancelWait := boundedCleanupContext(ctx)
	defer cancelWait()
	select {
	case status := <-waitResult:
		return status, nil
	case <-waitCtx.Done():
		return ExitStatus{}, fmt.Errorf("process: wait after cleanup did not complete: %w", waitCtx.Err())
	}
}

func (runner Runner) verifyDetachedTree(ctx context.Context, record state.TrackedProcessRecord, options RunOptions) error {
	if options.Mode != Detached {
		return nil
	}
	verifier, ok := runner.Cleaner.(ProcessCleanupVerifier)
	if !ok {
		return errors.New("process: cancellation cleanup cannot verify descendants drained")
	}
	alive, err := verifier.Exists(ctx, record)
	if err != nil {
		return fmt.Errorf("process: verify detached process tree: %w", err)
	}
	if alive {
		return errors.New("process: detached process tree still has descendants")
	}
	return nil
}

func cancellationSafetyError(child Child, record state.TrackedProcessRecord, mode ProcessMode, cause error) error {
	return &ProcessCleanupUnsafeError{PID: child.PID(), Mode: mode, Record: record, Cause: cause}
}

func (runner Runner) cleanupCancelledChild(ctx context.Context, child Child, record state.TrackedProcessRecord, mode ProcessMode) error {
	if runner.Cleaner == nil {
		return errors.New("process: cancellation cleanup is unavailable")
	}
	if !cleanupRecordUsable(record, child.PID(), mode) {
		cleaner, ok := runner.Cleaner.(ProcessUnknownCleaner)
		if !ok {
			return errors.New("process: cancellation cleanup identity is unavailable")
		}
		drained, err := cleaner.CleanupUnknown(ctx, child, mode, record)
		if err != nil {
			return err
		}
		if !drained {
			return errors.New("process: cancellation cleanup did not prove the child tree drained")
		}
		return nil
	}
	return runner.Cleaner.Cleanup(ctx, child, record)
}

func (runner Runner) postSpawnFailure(
	ctx context.Context,
	child Child,
	mode ProcessMode,
	record state.TrackedProcessRecord,
	primary error,
	stdout, stderr *TailBuffer,
	register RegisterFunc,
	command []string,
	handoffComplete func(),
) (RunResult, error) {
	result := RunResult{PID: child.PID(), Started: true, Record: record, Stdout: stdout.String(), Stderr: stderr.String()}
	if !cleanupRecordUsable(record, child.PID(), mode) {
		sentinel := unverifiedProcessRecord(child.PID(), mode, command)
		result.Record = sentinel
		cleanupErr := runner.cleanupUnknownChild(ctx, child, mode, sentinel, errors.New("process identity or group boundary is unavailable"), register)
		if handoffComplete != nil {
			handoffComplete()
		}
		return result, &ProcessSetupError{Cause: primary, Cleanup: cleanupErr}
	}
	cleanupErr := error(nil)
	if runner.Cleaner == nil {
		cleanupErr = errors.New("process: post-spawn cleanup is unavailable")
	} else {
		cleanupCtx, cancelCleanup := boundedCleanupContext(ctx)
		cleanupErr = runner.Cleaner.Cleanup(cleanupCtx, child, record)
		cancelCleanup()
	}
	if cleanupErr == nil {
		status, waitErr := waitChildAfterCleanup(ctx, child)
		cleanupErr = waitErr
		if cleanupErr == nil {
			cleanupErr = errors.Join(waitStatusError(status), status.BoundaryError)
		}
		if cleanupErr != nil {
			cleanupErr = cancellationSafetyError(child, record, mode, cleanupErr)
		}
		if cleanupErr == nil && mode == Detached {
			verifier, ok := runner.Cleaner.(ProcessCleanupVerifier)
			if !ok {
				cleanupErr = cancellationSafetyError(child, record, mode, errors.New("process: detached cleanup cannot verify descendants drained"))
			} else {
				verifyCtx, cancelVerify := boundedCleanupContext(ctx)
				alive, verifyErr := verifier.Exists(verifyCtx, record)
				cancelVerify()
				if verifyErr != nil {
					cleanupErr = cancellationSafetyError(child, record, mode, fmt.Errorf("process: verify detached cleanup: %w", verifyErr))
				} else if alive {
					cleanupErr = cancellationSafetyError(child, record, mode, errors.New("process: detached cleanup left descendants running"))
				}
			}
		}
	} else {
		retainAfterCleanupFailure(child)
		cleanupErr = cancellationSafetyError(child, record, mode, cleanupErr)
	}
	if handoffComplete != nil {
		handoffComplete()
	}
	return result, &ProcessSetupError{Cause: primary, Cleanup: cleanupErr}
}

// retainAfterCleanupFailure is the existing retained-child reaper tradeoff:
// fail-closed cleanup must return promptly when termination is unverifiable,
// while the child is still reaped eventually if it exits. The assignment's
// PID/identity remains the authority for later recovery.
func retainAfterCleanupFailure(child Child) { go child.Wait() }

func (runner Runner) cleanupUnknownChild(ctx context.Context, child Child, mode ProcessMode, record state.TrackedProcessRecord, cause error, register RegisterFunc) error {
	unsafeErr := &ProcessCleanupUnsafeError{PID: child.PID(), Mode: mode, Record: record, Cause: cause}
	cleanupCtx, cancelCleanup := boundedCleanupContext(ctx)
	defer cancelCleanup()
	var sentinelErr error
	if register != nil {
		if err := register(cleanupCtx, record); err != nil {
			sentinelErr = fmt.Errorf("persist unverified process sentinel: %w", err)
		}
	}
	cleaner, ok := runner.Cleaner.(ProcessUnknownCleaner)
	if !ok {
		retainAfterCleanupFailure(child)
		return errors.Join(unsafeErr, sentinelErr)
	}
	drained, cleanupErr := cleaner.CleanupUnknown(cleanupCtx, child, mode, record)
	if cleanupErr != nil || !drained {
		if cleanupErr == nil {
			cleanupErr = errors.New("process: child boundary did not prove tree drained")
		}
		retainAfterCleanupFailure(child)
		return errors.Join(unsafeErr, cleanupErr, sentinelErr)
	}
	status, waitErr := waitChildAfterCleanup(ctx, child)
	if waitErr != nil {
		return errors.Join(unsafeErr, waitErr, sentinelErr)
	}
	if waitErr := waitStatusError(status); waitErr != nil || status.BoundaryError != nil {
		return errors.Join(unsafeErr, waitErr, status.BoundaryError, sentinelErr)
	}
	return sentinelErr
}

func unverifiedProcessRecord(pid int, mode ProcessMode, command []string) state.TrackedProcessRecord {
	record := state.TrackedProcessRecord{
		PID:       int64(pid),
		Command:   append([]string(nil), command...),
		StartedAt: UnverifiedIdentityMarker,
	}
	if mode == Detached && runtime.GOOS != "windows" {
		group := int64(pid)
		record.GroupID = &group
	}
	return record
}

func waitStatusError(status ExitStatus) error {
	// exec.Cmd.Wait reports an ExitError for ordinary non-zero exits and
	// signaled children. Those are represented by Code/Signal. Other wait
	// errors, including stdout/stderr copy failures after exit code 0, are
	// infrastructure failures and must not be reported as success.
	if status.Err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(status.Err, &exitErr) {
		return nil
	}
	return &ProcessWaitError{Cause: status.Err}
}

func normalizeExitCode(status ExitStatus) int {
	if status.Signal != nil {
		if signal, ok := status.Signal.(syscall.Signal); ok {
			return 128 + int(signal)
		}
	}
	if status.Code >= 0 {
		return status.Code
	}
	return 1
}

func validateRunnerRecord(record state.TrackedProcessRecord, pid int, mode ProcessMode) error {
	if record.PID != int64(pid) || record.PID <= 0 || record.StartedAt == "" {
		return errors.New("process: describer returned an inexact process record")
	}
	if mode == Detached && runtime.GOOS != "windows" && (record.GroupID == nil || *record.GroupID != record.PID) {
		return errors.New("process: detached process group is unavailable or inexact")
	}
	return nil
}

func cleanupRecordUsable(record state.TrackedProcessRecord, pid int, mode ProcessMode) bool {
	if record.PID != int64(pid) || record.PID <= 0 || record.StartedAt == "" {
		return false
	}
	if mode == Detached && runtime.GOOS != "windows" {
		return record.GroupID != nil && *record.GroupID == record.PID
	}
	return true
}
