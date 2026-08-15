package cli

import (
	"context"
	"errors"

	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

// ShellProcessRunner is the native process boundary used by
// NativeShellTerminal. Keeping it injectable lets platform tests verify the
// session and identity handoff without launching a shell.
type ShellProcessRunner interface {
	Run(context.Context, []string, processpkg.RunOptions) (processpkg.RunResult, error)
}

// ShellProcessTracker proves that the shell leader and its descendants have
// drained. Implementations must fail closed when identity or enumeration is
// unavailable.
type ShellProcessTracker interface {
	Exists(context.Context, state.TrackedProcessRecord) (bool, error)
}

// ShellProcessRegister persists the exact shell process on its assignment
// before the runner begins waiting for it.
type ShellProcessRegister func(context.Context, string, state.TrackedProcessRecord) error

// ShellProcessRemove deletes the exact persisted process only after the
// native tree has been proven drained.
type ShellProcessRemove func(context.Context, string, state.TrackedProcessRecord) error

// ShellTerminalOptions configures the dependency-free native shell adapter.
// The default runner uses inherited stdio and a native process group/job; no
// util-linux, PowerShell, tasklist, or taskkill helper is started.
type ShellTerminalOptions struct {
	Runner   ShellProcessRunner
	Tracker  ShellProcessTracker
	Register ShellProcessRegister
	Remove   ShellProcessRemove
}

// NativeShellTerminal runs the selected shell through the existing native
// process and identity seams. It intentionally does not claim drained state
// until the exact process record has been checked after the leader exits.
type NativeShellTerminal struct {
	runner   ShellProcessRunner
	tracker  ShellProcessTracker
	register ShellProcessRegister
	remove   ShellProcessRemove
}

// NewNativeShellTerminal constructs a native terminal adapter. Nil seams use
// the platform implementations from internal/process.
func NewNativeShellTerminal(options ShellTerminalOptions) *NativeShellTerminal {
	runner := options.Runner
	if runner == nil {
		native := processpkg.NewRunner()
		runner = native
	}
	tracker := options.Tracker
	if tracker == nil {
		native := processpkg.NewNativeProcessManager()
		tracker = native
	}
	return &NativeShellTerminal{runner: runner, tracker: tracker, register: options.Register, remove: options.Remove}
}

// Run starts the shell in the workspace using inherited stdio. Detached mode
// provides the native process-group/job boundary needed to observe children;
// PTY/ConPTY allocation remains an explicit platform seam rather than an
// unsafe script or shell-helper fallback.
func (terminal *NativeShellTerminal) Run(ctx context.Context, request ShellTerminalRequest) (ShellTerminalResult, error) {
	if terminal == nil {
		return ShellTerminalResult{}, errors.New("shell terminal is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if request.AssignmentID == "" {
		return ShellTerminalResult{}, errors.New("shell terminal assignment must not be empty")
	}
	if request.WorkspacePath == "" {
		return ShellTerminalResult{}, errors.New("shell terminal workspace path must not be empty")
	}
	if request.Shell == "" {
		return ShellTerminalResult{}, errors.New("shell terminal executable must not be empty")
	}
	if terminal.runner == nil || terminal.tracker == nil || terminal.register == nil || terminal.remove == nil {
		return ShellTerminalResult{}, errors.New("shell terminal process dependencies are unavailable")
	}

	var registered state.TrackedProcessRecord
	run, err := terminal.runner.Run(ctx, []string{request.Shell}, processpkg.RunOptions{
		Dir:    request.WorkspacePath,
		Mode:   processpkg.Detached,
		Stdin:  request.Stdin,
		Stdout: request.Stdout,
		Stderr: request.Stderr,
		Register: func(registerCtx context.Context, record state.TrackedProcessRecord) error {
			if err := terminal.register(registerCtx, request.AssignmentID, record); err != nil {
				return err
			}
			registered = record
			return nil
		},
	})
	result := ShellTerminalResult{ExitCode: run.ExitCode, Signal: run.Signal}
	if err != nil {
		return result, err
	}
	if run.Record.PID <= 0 || run.Record.StartedAt == "" || registered.PID != run.Record.PID || registered.StartedAt != run.Record.StartedAt {
		return result, errors.New("shell terminal process identity is unavailable")
	}
	// Completion inspection must not be prevented by the command context being
	// canceled after the child exited; an uncertain tree remains retained.
	tracked, err := terminal.tracker.Exists(context.WithoutCancel(ctx), run.Record)
	if err != nil {
		return result, err
	}
	if tracked {
		return result, errors.New("shell terminal descendants are still running")
	}
	if err := terminal.remove(context.WithoutCancel(ctx), request.AssignmentID, run.Record); err != nil {
		return result, err
	}
	result.DescendantsDrained = true
	return result, nil
}
