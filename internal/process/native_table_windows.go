//go:build windows

package process

import (
	"context"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	th32csSnapProcess = uintptr(0x00000002)
	errorBadLength    = syscall.Errno(24)
	errorNoMoreFiles  = syscall.Errno(18)
)

var (
	createToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	process32FirstW          = kernel32.NewProc("Process32FirstW")
	process32NextW           = kernel32.NewProc("Process32NextW")
)

type processEntry32 struct {
	Size              uint32
	Usage             uint32
	ProcessID         uint32
	DefaultHeapID     uintptr
	ModuleID          uint32
	Threads           uint32
	ParentProcessID   uint32
	PriorityClassBase int32
	Flags             uint32
	ExeFile           [syscall.MAX_PATH]uint16
}

func snapshotPlatform(ctx context.Context) ([]Entry, error) {
	handle, err := createProcessSnapshot()
	if err != nil {
		return nil, err
	}
	defer closeHandle.Call(handle)

	current := processEntry32{Size: uint32(unsafe.Sizeof(processEntry32{}))}
	ok, _, callErr := process32FirstW.Call(handle, uintptr(unsafe.Pointer(&current)))
	if ok == 0 {
		if errno, _ := callErr.(syscall.Errno); errno == errorNoMoreFiles {
			return []Entry{}, nil
		}
		return nil, fmt.Errorf("read Windows process table: %w", callErr)
	}

	entries := make([]Entry, 0, 256)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if current.ProcessID > 0 {
			entries = append(entries, Entry{
				PID:       int(current.ProcessID),
				ParentPID: int(current.ParentProcessID),
			})
		}
		current.Size = uint32(unsafe.Sizeof(processEntry32{}))
		ok, _, callErr = process32NextW.Call(handle, uintptr(unsafe.Pointer(&current)))
		if ok != 0 {
			continue
		}
		if errno, _ := callErr.(syscall.Errno); errno == errorNoMoreFiles {
			break
		}
		return nil, fmt.Errorf("continue Windows process table: %w", callErr)
	}
	return entries, nil
}

func createProcessSnapshot() (uintptr, error) {
	for range 4 {
		handle, _, callErr := createToolhelp32Snapshot.Call(th32csSnapProcess, 0)
		if handle != ^uintptr(0) {
			return handle, nil
		}
		if errno, _ := callErr.(syscall.Errno); errno != errorBadLength {
			return 0, fmt.Errorf("snapshot Windows process table: %w", callErr)
		}
	}
	return 0, fmt.Errorf("snapshot Windows process table: %w", errorBadLength)
}
