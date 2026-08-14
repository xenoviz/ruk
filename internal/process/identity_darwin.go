//go:build darwin

package process

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/xenoviz/ruk/internal/lock"
)

// Darwin retains the established ps start-time representation while native
// libproc supervision is added in the next process-tree slice. Windows, where
// helper polling caused the resource storm, never launches a helper process.
func inspectPlatform(ctx context.Context, pid int) (lock.ProcessState, error) {
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if ctx.Err() != nil {
		return lock.ProcessState{}, ctx.Err()
	}
	identity := strings.Join(strings.Fields(string(output)), " ")
	if err == nil && identity != "" {
		return lock.ProcessState{Alive: true, IdentityKnown: true, Identity: identity}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && identity == "" {
		probeErr := syscall.Kill(pid, 0)
		switch {
		case probeErr == nil, errors.Is(probeErr, syscall.EPERM):
			return lock.ProcessState{Alive: true, IdentityKnown: false}, nil
		case errors.Is(probeErr, syscall.ESRCH):
			return lock.ProcessState{}, nil
		default:
			return unavailableIdentity(pid, fmt.Errorf("probe after /bin/ps failure: %w", probeErr))
		}
	}
	if err != nil {
		return unavailableIdentity(pid, fmt.Errorf("run /bin/ps: %w", err))
	}
	return lock.ProcessState{}, nil
}
