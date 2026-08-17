//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// configureCommand confines Windows command-line differences to this file.
// Batch files use the native command interpreter; no PowerShell process is
// involved.
func configureCommand(command *exec.Cmd, _ ProcessMode, _ int) error {
	if command == nil {
		return errors.New("process: Windows command is unavailable")
	}
	attributes := &syscall.SysProcAttr{HideWindow: true}
	// Keep the primary thread suspended until it has been assigned to the
	// kill-on-close Job Object. spawnWindowsProcess resumes it only after the
	// assignment succeeds.
	attributes.CreationFlags = createSuspended
	if isWindowsBatchFile(command.Path) {
		comspec := windowsComSpec(command.Env)
		if comspec == "" {
			return errors.New("process: COMSPEC is unavailable for batch command")
		}
		original := append([]string(nil), command.Args...)
		if len(original) == 0 {
			original = []string{command.Path}
		}
		batch := original[0]
		if batch == "" {
			batch = command.Path
		}
		args := append([]string(nil), original[1:]...)
		command.Path = comspec
		command.Args = append([]string{comspec, "/d", "/s", "/c", batch}, args...)
		// cmd.exe's /c parser requires an extra pair of quotes around a
		// quoted batch path.
		attributes.CmdLine = windowsBatchCommandLine(comspec, batch, args)
	}
	command.SysProcAttr = attributes
	return nil
}

func processExitStatus(state *os.ProcessState, waitErr error) ExitStatus {
	if state == nil {
		return ExitStatus{Code: -1, Err: waitErr}
	}
	return ExitStatus{Code: state.ExitCode(), Err: waitErr}
}

// Windows cleanup is carried by windowsManagedChild's Job Object; there is
// no portable process-group signaler to install in NativeProcessCleaner.
func defaultGroupSignaler() GroupSignaler { return nil }

func spawnOSProcess(ctx context.Context, request SpawnRequest) (Child, error) {
	return spawnWindowsProcess(ctx, request)
}

// spawnWindowsProcess is the native implementation used by the shared
// OSProcessSpawner. The Job Object is created before CreateProcess and the
// child is assigned before this function returns.
func spawnWindowsProcess(ctx context.Context, request SpawnRequest) (Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Command == "" {
		return nil, errors.New("process: command must not be empty")
	}
	command := exec.CommandContext(ctx, request.Command, request.Args...)
	command.Dir = request.Dir
	command.Env = request.Env
	command.Stdin = request.Stdin
	command.Stdout = request.Stdout
	command.Stderr = request.Stderr
	if err := configureCommand(command, request.Mode, -1); err != nil {
		return nil, err
	}
	job, err := newWindowsJob()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = job.Close()
		return nil, err
	}
	if err := job.AssignProcess(command.Process.Pid); err != nil {
		// command.Process owns the exact process handle returned by CreateProcess;
		// kill it before Wait so a failed assignment cannot leave a suspended
		// process behind while an empty job is closed.
		killErr := command.Process.Kill()
		_ = job.Close()
		waited, waitErr := waitCommandAfterCleanup(command)
		if !waited {
			record := unverifiedProcessRecord(command.Process.Pid, request.Mode, append([]string{request.Command}, request.Args...))
			return nil, errors.Join(
				fmt.Errorf("process: assign child %d to Windows job: %w; terminate exact child: %v", command.Process.Pid, err, killErr),
				&ProcessCleanupUnsafeError{PID: command.Process.Pid, Mode: request.Mode, Record: record, Cause: waitErr},
			)
		}
		if killErr != nil {
			return nil, fmt.Errorf("process: assign child %d to Windows job: %w; terminate exact child: %v", command.Process.Pid, err, killErr)
		}
		return nil, fmt.Errorf("process: assign child %d to Windows job: %w", command.Process.Pid, err)
	}
	if err := resumeWindowsProcess(command.Process.Pid); err != nil {
		// A resumed process is never handed back unless the resume operation
		// succeeded. Job termination covers descendants; Kill is the exact
		// process-handle fallback if job termination itself is unavailable.
		terminateErr := job.Terminate()
		killErr := command.Process.Kill()
		_ = job.Close()
		waited, waitErr := waitCommandAfterCleanup(command)
		if !waited {
			record := unverifiedProcessRecord(command.Process.Pid, request.Mode, append([]string{request.Command}, request.Args...))
			return nil, errors.Join(
				fmt.Errorf("process: resume child %d after Windows job assignment: %w; terminate job: %v; terminate exact child: %v", command.Process.Pid, err, terminateErr, killErr),
				&ProcessCleanupUnsafeError{PID: command.Process.Pid, Mode: request.Mode, Record: record, Cause: waitErr},
			)
		}
		if terminateErr != nil || killErr != nil {
			return nil, fmt.Errorf("process: resume child %d after Windows job assignment: %w; terminate job: %v; terminate exact child: %v", command.Process.Pid, err, terminateErr, killErr)
		}
		return nil, fmt.Errorf("process: resume child %d after Windows job assignment: %w", command.Process.Pid, err)
	}
	return &windowsManagedChild{command: command, job: job, ctx: ctx}, nil
}

func waitCommandAfterCleanup(command *exec.Cmd) (bool, error) {
	result := make(chan error, 1)
	go func() { result <- command.Wait() }()
	waitCtx, cancelWait := boundedCleanupContext(context.Background())
	defer cancelWait()
	select {
	case <-result:
		return true, nil
	case <-waitCtx.Done():
		return false, fmt.Errorf("process: Windows child wait after cleanup did not complete: %w", waitCtx.Err())
	}
}

type windowsManagedChild struct {
	command  *exec.Cmd
	job      *windowsJob
	ctx      context.Context
	waitOnce sync.Once
	status   ExitStatus
}

func (child *windowsManagedChild) PID() int { return child.command.Process.Pid }

func (child *windowsManagedChild) Wait() ExitStatus {
	child.waitOnce.Do(func() {
		waitErr := child.command.Wait()
		child.status = processExitStatus(child.command.ProcessState, waitErr)
		// Waiting for the leader is deliberately unbounded: a live command must
		// not be killed merely because it has run for a long time. Once the
		// leader exits, however, descendants must drain within a bounded cleanup
		// window or the owning assignment is retained for recovery.
		emptyCtx, cancelEmpty := boundedCleanupContext(child.ctx)
		emptyErr := waitWindowsJobEmptyBounded(child.job, emptyCtx)
		cancelEmpty()
		if emptyErr != nil {
			if child.status.Err == nil {
				child.status.Err = emptyErr
			} else {
				child.status.BoundaryError = errors.Join(child.status.BoundaryError, emptyErr)
			}
		}
		if closeErr := child.job.Close(); closeErr != nil {
			if child.status.Err == nil {
				child.status.Err = closeErr
			} else {
				child.status.BoundaryError = errors.Join(child.status.BoundaryError, closeErr)
			}
		}
	})
	return child.status
}

func waitWindowsJobEmptyBounded(job *windowsJob, ctx context.Context) error {
	result := make(chan error, 1)
	go func() { result <- job.WaitEmpty(ctx) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("process: Windows Job Object descendants did not drain: %w", ctx.Err())
	}
}

func (child *windowsManagedChild) Signal(signal os.Signal) error {
	if signal == nil {
		return errors.New("process: nil Windows process signal")
	}
	// Windows has no portable graceful process signal. Terminate the job,
	// never fall back to a PID-only signal that would orphan descendants.
	return child.job.Terminate()
}

func isWindowsBatchFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cmd" || ext == ".bat"
}

func windowsComSpec(environment []string) string {
	if value := windowsEnvironmentValue(environment, "COMSPEC"); value != "" {
		return value
	}
	if value := os.Getenv("COMSPEC"); value != "" {
		return value
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "cmd.exe")
}

func windowsEnvironmentValue(environment []string, name string) string {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func windowsBatchCommandLine(comspec, batch string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, syscall.EscapeArg(batch))
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return syscall.EscapeArg(comspec) + " /d /s /c \"" + strings.Join(parts, " ") + "\""
}
