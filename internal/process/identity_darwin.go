//go:build darwin

package process

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	_ "unsafe"

	"github.com/xenoviz/ruk/internal/lock"
)

const darwinTimevalSize = 16

const (
	darwinCTLKern     = int32(1)
	darwinKernProc    = int32(14)
	darwinKernProcPID = int32(1)
)

// syscall.sysctl is the Go standard library's native libc sysctl binding, but
// it is intentionally unexported. Link to that binding so Ruk can pass the
// numeric process MIB directly without cgo, a shell helper, or a third-party
// syscall package.
//
//go:linkname darwinSysctl syscall.sysctl
func darwinSysctl(mib []int32, old *byte, oldlen *uintptr, new *byte, newlen uintptr) error

// inspectPlatform reads the process start timeval from Darwin's native
// kern.proc sysctl. Unlike ps lstart, the microsecond component prevents two
// successive processes reusing a PID in the same second from sharing an
// identity. If the native record cannot be decoded, the process remains live
// but its identity is deliberately unavailable so callers fail closed.
func inspectPlatform(ctx context.Context, pid int) (lock.ProcessState, error) {
	value, err := readDarwinKinfoProc(pid)
	if ctx.Err() != nil {
		return lock.ProcessState{}, ctx.Err()
	}
	if err == nil {
		identity, parseErr := parseDarwinStartTime(value)
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

func readDarwinKinfoProc(pid int) ([]byte, error) {
	mib, err := darwinKinfoProcMIB(pid)
	if err != nil {
		return nil, err
	}
	var size uintptr
	if err := darwinSysctl(mib, nil, &size, nil, 0); err != nil {
		return nil, err
	}
	if size < darwinTimevalSize {
		return nil, fmt.Errorf("native process record is %d bytes, need at least %d", size, darwinTimevalSize)
	}
	buffer := make([]byte, size)
	if err := darwinSysctl(mib, &buffer[0], &size, nil, 0); err != nil {
		return nil, err
	}
	if size < darwinTimevalSize || size > uintptr(len(buffer)) {
		return nil, fmt.Errorf("native process record size changed to %d", size)
	}
	return buffer[:size], nil
}

func darwinKinfoProcMIB(pid int) ([]int32, error) {
	if pid <= 0 || int64(int32(pid)) != int64(pid) {
		return nil, errors.New("invalid Darwin process ID")
	}
	return []int32{darwinCTLKern, darwinKernProc, darwinKernProcPID, int32(pid)}, nil
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
