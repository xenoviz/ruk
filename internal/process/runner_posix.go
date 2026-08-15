//go:build !windows

package process

import (
	"os"
	"os/exec"
	"syscall"
)

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
