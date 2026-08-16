//go:build !windows

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func spawnOSProcess(ctx context.Context, request SpawnRequest) (Child, error) {
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
	foreground, err := foregroundTerminalFor(request.Stdin, request.ForegroundTerminal)
	if err != nil {
		return nil, err
	}
	if foreground != nil && request.Mode != Detached {
		modeErr := errors.New("process: foreground terminal requires detached mode")
		if restoreErr := foreground.restore(); restoreErr != nil {
			return nil, errors.Join(modeErr, restoreErr)
		}
		return nil, modeErr
	}
	foregroundFD := -1
	if foreground != nil {
		foregroundFD = foreground.fd
	}
	if err := configureCommand(command, request.Mode, foregroundFD); err != nil {
		if foreground != nil {
			if restoreErr := foreground.restore(); restoreErr != nil {
				return nil, errors.Join(err, restoreErr)
			}
		}
		return nil, err
	}
	if err := command.Start(); err != nil {
		if foreground != nil {
			if restoreErr := foreground.restore(); restoreErr != nil {
				return nil, errors.Join(err, restoreErr)
			}
		}
		return nil, err
	}
	return &osChild{command: command, foreground: foreground}, nil
}

func configureCommand(command *exec.Cmd, mode ProcessMode, foregroundFD int) error {
	if mode == Detached {
		attributes := &syscall.SysProcAttr{Setpgid: true}
		if foregroundFD >= 0 {
			attributes.Foreground = true
			attributes.Ctty = foregroundFD
		}
		command.SysProcAttr = attributes
	}
	return nil
}

func processExitStatus(state *os.ProcessState, waitErr error) ExitStatus {
	if state == nil {
		return ExitStatus{Code: -1, Err: waitErr}
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return ExitStatus{Code: state.ExitCode(), Err: waitErr}
	}
	if status.Signaled() {
		return ExitStatus{Code: -1, Signal: status.Signal(), Err: waitErr}
	}
	return ExitStatus{Code: status.ExitStatus(), Err: waitErr}
}

func defaultGroupSignaler() GroupSignaler { return NativeGroupSignaler{} }
