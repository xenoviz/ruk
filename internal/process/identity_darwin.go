//go:build darwin

package process

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"syscall"

	"github.com/xenoviz/ruk/internal/lock"
)

const darwinTimevalSize = 16

// inspectPlatform reads the process start timeval from Darwin's native
// kern.proc sysctl. Unlike ps lstart, the microsecond component prevents two
// successive processes reusing a PID in the same second from sharing an
// identity. If the native record cannot be decoded, the process remains live
// but its identity is deliberately unavailable so callers fail closed.
func inspectPlatform(ctx context.Context, pid int) (lock.ProcessState, error) {
	value, err := syscall.Sysctl("kern.proc.pid." + strconv.Itoa(pid))
	if ctx.Err() != nil {
		return lock.ProcessState{}, ctx.Err()
	}
	if err == nil {
		identity, parseErr := parseDarwinStartTime([]byte(value))
		if parseErr == nil {
			return lock.ProcessState{Alive: true, IdentityKnown: true, Identity: identity}, nil
		}
		err = fmt.Errorf("parse native process record: %w", parseErr)
	}
	probeErr := syscall.Kill(pid, 0)
	switch {
	case probeErr == nil, errors.Is(probeErr, syscall.EPERM):
		return unavailableIdentity(pid, fmt.Errorf("read native process record: %w", err))
	case errors.Is(probeErr, syscall.ESRCH):
		return lock.ProcessState{}, nil
	default:
		return unavailableIdentity(pid, fmt.Errorf("probe after native process record failure: %w", probeErr))
	}
}

func parseDarwinStartTime(record []byte) (string, error) {
	if len(record) < darwinTimevalSize {
		return "", fmt.Errorf("native process record is %d bytes, need at least %d", len(record), darwinTimevalSize)
	}
	seconds := int64(binary.LittleEndian.Uint64(record[:8]))
	microseconds := int64(int32(binary.LittleEndian.Uint32(record[8:12])))
	if seconds <= 0 {
		return "", errors.New("native process start time has invalid seconds")
	}
	if microseconds < 0 || microseconds >= 1_000_000 {
		return "", errors.New("native process start time has invalid microseconds")
	}
	return fmt.Sprintf("darwin:%d:%d", seconds, microseconds), nil
}
