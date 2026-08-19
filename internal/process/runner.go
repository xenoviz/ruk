package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

const processCleanupTimeout = 30 * time.Second

// ProcessMode describes whether a managed child is attached to the caller or
// owns a separately signalable process group.
type ProcessMode uint8

const (
	Attached ProcessMode = iota
	Detached
)

// SpawnRequest is the small, platform-neutral contract used by Runner.
type SpawnRequest struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
	Mode    ProcessMode
	// ForegroundTerminal asks a detached POSIX child to become the foreground
	// process group of a detected controlling terminal. Windows ignores this
	// flag because its Job Object remains the process-tree boundary.
	ForegroundTerminal bool
	Stdin              io.Reader
	Stdout             io.Writer
	Stderr             io.Writer
}

// ExitStatus is the result returned by a spawned process. Code is normalized
// by Runner to the conventional 128+signal value when Signal is non-nil.
type ExitStatus struct {
	Code          int
	Signal        os.Signal
	Err           error
	BoundaryError error
}

// Child is the minimal operating-system process boundary needed by Runner.
type Child interface {
	PID() int
	Wait() ExitStatus
	Signal(os.Signal) error
}

// Spawner starts one child with context-aware launch semantics.
type Spawner interface {
	Spawn(context.Context, SpawnRequest) (Child, error)
}

// ProcessDescriber turns a just-created child into an exact durable record.
// Implementations must fail when the leader identity cannot be established.
type ProcessDescriber interface {
	Describe(context.Context, int, ProcessMode, []string) (state.TrackedProcessRecord, error)
}

// RegisterFunc persists a process record before the runner hands control back
// to its caller. Registration errors trigger fail-closed cleanup.
type RegisterFunc func(context.Context, state.TrackedProcessRecord) error

// ProcessCleaner is used only when registration fails. It must retain the
// process when identity or group membership cannot be verified.
type ProcessCleaner interface {
	Cleanup(context.Context, Child, state.TrackedProcessRecord) error
}

// ProcessUnknownCleaner is an optional native boundary for a child whose
// durable identity could not be described. Drained is true only when the
// implementation owns a stronger exact boundary (for example a Windows Job
// Object) and has terminated the complete tree.
type ProcessUnknownCleaner interface {
	CleanupUnknown(context.Context, Child, ProcessMode, state.TrackedProcessRecord) (drained bool, err error)
}

// ProcessCleanupVerifier proves that a previously cleaned detached tree has
// fully drained. Signaling a group does not itself wait for descendants.
type ProcessCleanupVerifier interface {
	Exists(context.Context, state.TrackedProcessRecord) (bool, error)
}

// SignalForwarder is an explicit hook for wrapper signal forwarding. Keeping
// it injected lets the CLI own signal subscriptions without coupling process
// execution to global os/signal state.
type SignalForwarder interface {
	Forward(context.Context, state.TrackedProcessRecord, os.Signal) error
}

// RunOptions controls one managed command.
type RunOptions struct {
	Dir                string
	Env                []string
	Mode               ProcessMode
	ForegroundTerminal bool
	// SuperviseCancellation keeps the spawned child context alive while the
	// caller context is observed separately. On cancellation, Runner uses the
	// native cleaner to terminate the full process boundary, waits for the
	// leader, and verifies detached descendants have drained before returning.
	// Callers must retain their owning resource when this proof fails.
	SuperviseCancellation bool
	Stdin                 io.Reader
	Stdout                io.Writer
	Stderr                io.Writer
	CaptureLimit          int
	Register              RegisterFunc
	// HandoffComplete is called after registration succeeds, or after failed
	// registration cleanup has settled. Managed callers use it to release the
	// workspace handoff lock before Runner waits for a long-lived child.
	HandoffComplete func()
}

// RunResult contains the normalized child result and bounded diagnostic tails.
type RunResult struct {
	PID int
	// Started is true once a spawner returned a non-nil child. It distinguishes
	// an ordinary pre-spawn failure from an unsafe post-spawn failure where the
	// PID or identity could not be established.
	Started  bool
	ExitCode int
	Signal   os.Signal
	Stdout   string
	Stderr   string
	Record   state.TrackedProcessRecord
}

// RegistrationError preserves both the registration failure and any cleanup
// failure. A cleanup failure means the process remains owned for recovery.
type RegistrationError struct {
	Register error
	Cleanup  error
}

// ProcessSetupError reports a failure after the child was started but before
// registration could hand it to the caller. A non-nil Cleanup means the full
// child boundary was not proven drained; callers must retain the assignment,
// using the durable unverified sentinel when one was registered.
type ProcessSetupError struct {
	Cause   error
	Cleanup error
}

// ProcessCleanupUnsafeError means that a child was started, but its exact
// identity or process-group boundary could not be established. The runner
// cannot safely signal that child, so callers must retain the owning
// assignment for recovery instead of releasing the workspace.
type ProcessCleanupUnsafeError struct {
	PID    int
	Mode   ProcessMode
	Record state.TrackedProcessRecord
	Cause  error
}

func (err *ProcessCleanupUnsafeError) Error() string {
	if err.Cause == nil {
		return fmt.Sprintf("process: cleanup for child %d is unsafe", err.PID)
	}
	return fmt.Sprintf("process: cleanup for child %d is unsafe: %v", err.PID, err.Cause)
}

func (err *ProcessCleanupUnsafeError) Unwrap() error { return err.Cause }

// ProcessWaitError means the child did not produce a trustworthy exit status.
// Ordinary non-zero exits are deliberately not wrapped this way.
type ProcessWaitError struct{ Cause error }

func (err *ProcessWaitError) Error() string {
	if err == nil || err.Cause == nil {
		return "process: child wait failed"
	}
	return fmt.Sprintf("process: child wait failed: %v", err.Cause)
}

func (err *ProcessWaitError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func (err *ProcessSetupError) Error() string {
	if err.Cleanup == nil {
		return fmt.Sprintf("prepare managed process: %v", err.Cause)
	}
	return fmt.Sprintf("prepare managed process: %v; cleanup failed: %v", err.Cause, err.Cleanup)
}

func (err *ProcessSetupError) Unwrap() []error {
	if err.Cleanup == nil {
		return []error{err.Cause}
	}
	return []error{err.Cause, err.Cleanup}
}

func (err *RegistrationError) Error() string {
	if err.Cleanup == nil {
		return fmt.Sprintf("register managed process: %v", err.Register)
	}
	return fmt.Sprintf("register managed process: %v; cleanup failed: %v", err.Register, err.Cleanup)
}

func (err *RegistrationError) Unwrap() []error {
	if err.Cleanup == nil {
		return []error{err.Register}
	}
	return []error{err.Register, err.Cleanup}
}

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

// ForwardSignal invokes the configured forwarding hook for a registered
// record. Callers can invoke it from an os/signal subscription they own.
func (runner Runner) ForwardSignal(ctx context.Context, record state.TrackedProcessRecord, signal os.Signal) error {
	if runner.Forwarder == nil {
		return errors.New("process: signal forwarder is unavailable")
	}
	return runner.Forwarder.Forward(ctx, record, signal)
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

// NativeProcessDescriber uses the existing native identity and process table
// seams. POSIX detached children must be their own process-group leaders.
type NativeProcessDescriber struct {
	Probe lock.ProcessProbe
	Table ProcessTable
}

func (describer NativeProcessDescriber) Describe(ctx context.Context, pid int, mode ProcessMode, command []string) (state.TrackedProcessRecord, error) {
	if describer.Probe == nil {
		return state.TrackedProcessRecord{}, errors.New("process: identity probe is unavailable")
	}
	observed, err := describer.Probe.Inspect(ctx, pid)
	if err != nil {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: err}
	}
	if !observed.Alive || !observed.IdentityKnown || observed.Identity == "" {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: errors.New("process identity is unavailable")}
	}
	record := state.TrackedProcessRecord{PID: int64(pid), Command: append([]string(nil), command...), StartedAt: observed.Identity}
	if mode != Detached || runtime.GOOS == "windows" {
		return record, nil
	}
	if describer.Table == nil {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: errors.New("process table is unavailable")}
	}
	entries, err := describer.Table.Snapshot(ctx)
	if err != nil {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: err}
	}
	for _, entry := range entries {
		if entry.PID == pid && entry.GroupID == pid {
			group := int64(pid)
			record.GroupID = &group
			return record, nil
		}
	}
	return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: errors.New("detached child is not its own process-group leader")}
}

// NativeProcessCleaner identity-checks an attached child before signaling it,
// and delegates detached cleanup to the existing identity-fenced terminator.
type NativeProcessCleaner struct {
	Probe    lock.ProcessProbe
	Table    ProcessTable
	Signaler GroupSignaler
}

func (cleaner NativeProcessCleaner) Cleanup(ctx context.Context, child Child, record state.TrackedProcessRecord) error {
	if cleaner.Probe == nil || child == nil {
		return &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("process cleanup dependency is unavailable")}
	}
	if IsUnverifiedRecord(record) {
		return &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("unverified process boundary cannot be signaled")}
	}
	if record.GroupID != nil {
		if cleaner.Signaler == nil {
			return &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("process-group signaler is unavailable")}
		}
		table := cleaner.Table
		if table == nil {
			table = NativeTable{}
		}
		terminated, err := (GroupTerminator{Probe: cleaner.Probe, Table: table, Signaler: cleaner.Signaler}).TerminateGroup(ctx, record, true)
		if err != nil {
			return err
		}
		if !terminated {
			return nil
		}
		return nil
	}
	observed, err := cleaner.Probe.Inspect(ctx, int(record.PID))
	if err != nil || !observed.Alive || !observed.IdentityKnown || !exactIdentityMatch(record.StartedAt, observed.Identity) {
		if err == nil {
			err = errors.New("process identity changed before registration cleanup")
		}
		return &IdentityUnavailableError{PID: int(record.PID), Cause: err}
	}
	// Recheck immediately before signaling: the first probe can race PID reuse.
	revalidated, err := cleaner.Probe.Inspect(ctx, int(record.PID))
	if err != nil || !revalidated.Alive || !revalidated.IdentityKnown || !exactIdentityMatch(record.StartedAt, revalidated.Identity) {
		if err == nil {
			err = errors.New("process identity changed before registration cleanup signal")
		}
		return &IdentityUnavailableError{PID: int(record.PID), Cause: err}
	}
	if err := child.Signal(os.Kill); err != nil {
		return fmt.Errorf("terminate process %d: %w", record.PID, err)
	}
	return nil
}

// CleanupUnknown uses an exact child-owned boundary only where the platform
// provides one. POSIX detached children require a verified group identity and
// therefore remain retained when description failed.
func (cleaner NativeProcessCleaner) CleanupUnknown(ctx context.Context, child Child, mode ProcessMode, record state.TrackedProcessRecord) (bool, error) {
	if child == nil {
		return false, errors.New("process: unknown-child cleanup dependency is unavailable")
	}
	if runtime.GOOS == "windows" {
		if err := child.Signal(os.Kill); err != nil {
			return false, fmt.Errorf("terminate unknown Windows child boundary: %w", err)
		}
		return true, nil
	}
	if mode == Attached {
		if err := child.Signal(os.Kill); err != nil {
			return false, fmt.Errorf("terminate unknown attached child: %w", err)
		}
		// Killing the exact leader does not prove that an attached child did
		// not create descendants in the supervisor's process group.
		return false, nil
	}
	return false, &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("detached process group identity is unavailable")}
}

// Exists implements ProcessCleanupVerifier for native registration cleanup.
func (cleaner NativeProcessCleaner) Exists(ctx context.Context, record state.TrackedProcessRecord) (bool, error) {
	manager := NewNativeProcessManager(ReleaseManagerOptions{Probe: cleaner.Probe, Table: cleaner.Table})
	return manager.Exists(ctx, record)
}

// TailBuffer retains only the final Limit bytes, making diagnostics bounded
// even when a failed installer emits an unbounded stream.
type TailBuffer struct {
	limit     int
	data      []byte
	truncated bool
}

func NewTailBuffer(limit int) *TailBuffer { return &TailBuffer{limit: limit} }

func (buffer *TailBuffer) Write(data []byte) (int, error) {
	if buffer.limit <= 0 {
		if len(data) > 0 {
			buffer.truncated = true
		}
		return len(data), nil
	}
	if len(data) > buffer.limit {
		buffer.data = append(buffer.data[:0], data[len(data)-buffer.limit:]...)
		buffer.truncated = true
		return len(data), nil
	}
	if len(data) == buffer.limit {
		buffer.data = append(buffer.data[:0], data...)
		return len(data), nil
	}
	if len(buffer.data)+len(data) > buffer.limit {
		drop := len(buffer.data) + len(data) - buffer.limit
		buffer.data = append(buffer.data[:0], buffer.data[drop:]...)
		buffer.truncated = true
	}
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}

func (buffer *TailBuffer) String() string { return string(buffer.data) }

func (buffer *TailBuffer) Truncated() bool { return buffer.truncated }

func outputWriter(capture *TailBuffer, destination io.Writer) io.Writer {
	if destination == nil {
		return capture
	}
	return io.MultiWriter(capture, destination)
}

// OSProcessSpawner is the default child spawner. Platform-specific command
// attributes are confined to configureCommand in runner_*.go.
type OSProcessSpawner struct{}

func (OSProcessSpawner) Spawn(ctx context.Context, request SpawnRequest) (Child, error) {
	return spawnOSProcess(ctx, request)
}

type osChild struct {
	command    *exec.Cmd
	foreground *foregroundTerminal
}

func (child *osChild) PID() int { return child.command.Process.Pid }

func (child *osChild) Wait() ExitStatus {
	err := child.command.Wait()
	status := processExitStatus(child.command.ProcessState, err)
	if child.foreground != nil {
		if restoreErr := child.foreground.restore(); restoreErr != nil {
			status.BoundaryError = restoreErr
		}
	}
	return status
}

func (child *osChild) Signal(signal os.Signal) error { return child.command.Process.Signal(signal) }
