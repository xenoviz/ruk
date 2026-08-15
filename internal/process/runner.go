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

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

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
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// ExitStatus is the result returned by a spawned process. Code is normalized
// by Runner to the conventional 128+signal value when Signal is non-nil.
type ExitStatus struct {
	Code   int
	Signal os.Signal
	Err    error
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

// SignalForwarder is an explicit hook for wrapper signal forwarding. Keeping
// it injected lets the CLI own signal subscriptions without coupling process
// execution to global os/signal state.
type SignalForwarder interface {
	Forward(context.Context, state.TrackedProcessRecord, os.Signal) error
}

// RunOptions controls one managed command.
type RunOptions struct {
	Dir          string
	Env          []string
	Mode         ProcessMode
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	CaptureLimit int
	Register     RegisterFunc
}

// RunResult contains the normalized child result and bounded diagnostic tails.
type RunResult struct {
	PID      int
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
// registration could hand it to the caller. Cleanup is present only when an
// exact, mode-compatible record was available; otherwise the PID and record
// in RunResult are retained for the owning assignment to recover safely.
type ProcessSetupError struct {
	Cause   error
	Cleanup error
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

// NewRunner returns a native runner. Windows intentionally has no Job Object
// implementation in this slice; detached intent is retained in the contract,
// while platform cleanup remains identity-fenced.
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
	request := SpawnRequest{
		Command: command[0], Args: append([]string(nil), command[1:]...),
		Dir: options.Dir, Env: append([]string(nil), options.Env...), Mode: options.Mode,
		Stdin:  options.Stdin,
		Stdout: outputWriter(stdout, options.Stdout),
		Stderr: outputWriter(stderr, options.Stderr),
	}
	child, err := runner.Spawner.Spawn(ctx, request)
	if err != nil {
		return RunResult{}, err
	}
	if child == nil || child.PID() <= 0 {
		return RunResult{}, errors.New("process: spawner returned an invalid child")
	}
	record, err := runner.Describer.Describe(ctx, child.PID(), options.Mode, command)
	if err != nil {
		return runner.postSpawnFailure(ctx, child, options.Mode, record, err, stdout, stderr)
	}
	if err := validateRunnerRecord(record, child.PID(), options.Mode); err != nil {
		return runner.postSpawnFailure(ctx, child, options.Mode, record, err, stdout, stderr)
	}
	if options.Register != nil {
		if err := options.Register(ctx, record); err != nil {
			cleanupErr := error(nil)
			if runner.Cleaner == nil {
				cleanupErr = errors.New("process: registration cleanup is unavailable")
			} else {
				cleanupCtx := context.WithoutCancel(ctx)
				cleanupErr = runner.Cleaner.Cleanup(cleanupCtx, child, record)
			}
			if cleanupErr == nil {
				// Successful cleanup must reap the child before this handoff fails.
				_ = child.Wait()
			} else {
				retainAfterCleanupFailure(child)
			}
			return RunResult{PID: child.PID(), Record: record, Stdout: stdout.String(), Stderr: stderr.String()}, &RegistrationError{Register: err, Cleanup: cleanupErr}
		}
	}
	status := child.Wait()
	result := RunResult{
		PID: child.PID(), ExitCode: normalizeExitCode(status), Signal: status.Signal,
		Stdout: stdout.String(), Stderr: stderr.String(), Record: record,
	}
	return result, nil
}

func (runner Runner) postSpawnFailure(
	ctx context.Context,
	child Child,
	mode ProcessMode,
	record state.TrackedProcessRecord,
	primary error,
	stdout, stderr *TailBuffer,
) (RunResult, error) {
	result := RunResult{PID: child.PID(), Record: record, Stdout: stdout.String(), Stderr: stderr.String()}
	if !cleanupRecordUsable(record, child.PID(), mode) {
		return result, primary
	}
	cleanupErr := error(nil)
	if runner.Cleaner == nil {
		cleanupErr = errors.New("process: post-spawn cleanup is unavailable")
	} else {
		cleanupErr = runner.Cleaner.Cleanup(context.WithoutCancel(ctx), child, record)
	}
	if cleanupErr == nil {
		_ = child.Wait()
	} else {
		retainAfterCleanupFailure(child)
	}
	return result, &ProcessSetupError{Cause: primary, Cleanup: cleanupErr}
}

// retainAfterCleanupFailure is the existing retained-child reaper tradeoff:
// fail-closed cleanup must return promptly when termination is unverifiable,
// while the child is still reaped eventually if it exits. The assignment's
// PID/identity remains the authority for later recovery.
func retainAfterCleanupFailure(child Child) { go child.Wait() }

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
	if err != nil || !observed.Alive || !observed.IdentityKnown || observed.Identity != record.StartedAt {
		if err == nil {
			err = errors.New("process identity changed before registration cleanup")
		}
		return &IdentityUnavailableError{PID: int(record.PID), Cause: err}
	}
	if err := child.Signal(os.Kill); err != nil {
		return fmt.Errorf("terminate process %d: %w", record.PID, err)
	}
	return nil
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

type osChild struct{ command *exec.Cmd }

func (child osChild) PID() int { return child.command.Process.Pid }

func (child osChild) Wait() ExitStatus {
	err := child.command.Wait()
	return processExitStatus(child.command.ProcessState, err)
}

func (child osChild) Signal(signal os.Signal) error { return child.command.Process.Signal(signal) }
