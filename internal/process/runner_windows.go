//go:build windows

package process

import (
	"os"
	"os/exec"
)

func configureCommand(_ *exec.Cmd, _ ProcessMode) error { return nil }

func processExitStatus(state *os.ProcessState, waitErr error) ExitStatus {
	if state == nil {
		return ExitStatus{Code: -1, Err: waitErr}
	}
	return ExitStatus{Code: state.ExitCode(), Err: waitErr}
}

// Windows Job Objects are intentionally deferred; identity-fenced direct
// child cleanup remains the only native cleanup boundary in this slice.
func defaultGroupSignaler() GroupSignaler { return nil }
