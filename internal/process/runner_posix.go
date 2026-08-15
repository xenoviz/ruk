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
	if err := configureCommand(command, request.Mode); err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return osChild{command: command}, nil
}

func configureCommand(command *exec.Cmd, mode ProcessMode) error {
	if mode == Detached {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
