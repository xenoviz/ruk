//go:build windows

package process

import (
	"context"
	"fmt"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/xenoviz/ruk/internal/lock"
)

const (
	processQueryLimitedInformation = uintptr(0x1000)
	errorInvalidParameter          = syscall.Errno(87)
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	openProcess     = kernel32.NewProc("OpenProcess")
	getProcessTimes = kernel32.NewProc("GetProcessTimes")
	closeHandle     = kernel32.NewProc("CloseHandle")
)

type filetime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

func inspectPlatform(ctx context.Context, pid int) (lock.ProcessState, error) {
	if err := ctx.Err(); err != nil {
		return lock.ProcessState{}, err
	}
	handle, _, callErr := openProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		errno, _ := callErr.(syscall.Errno)
		switch errno {
		case errorInvalidParameter:
			return lock.ProcessState{}, nil
		case syscall.ERROR_ACCESS_DENIED:
			return lock.ProcessState{Alive: true, IdentityKnown: false}, nil
		default:
			return unavailableIdentity(pid, callErr)
		}
	}
	defer closeHandle.Call(handle)

	var created, exited, kernel, user filetime
	ok, _, callErr := getProcessTimes.Call(
		handle,
		uintptr(unsafe.Pointer(&created)),
		uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ok == 0 {
		return unavailableIdentity(pid, callErr)
	}
	raw := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if raw > ^uint64(0)-dotNetEpochOffset {
		return unavailableIdentity(pid, fmt.Errorf("creation FILETIME overflows .NET ticks"))
	}
	return lock.ProcessState{
		Alive:         true,
		IdentityKnown: true,
		Identity:      strconv.FormatUint(dotNetTicks(raw), 10),
	}, nil
}
