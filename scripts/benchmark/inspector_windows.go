//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	th32csSnapProcess              = 0x00000002
	processQueryLimitedInformation = 0x1000
	processVMRead                  = 0x0010
	maxPath                        = 260
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	psapi                    = syscall.NewLazyDLL("psapi.dll")
	createToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	process32FirstW          = kernel32.NewProc("Process32FirstW")
	process32NextW           = kernel32.NewProc("Process32NextW")
	openProcess              = kernel32.NewProc("OpenProcess")
	closeHandle              = kernel32.NewProc("CloseHandle")
	getProcessMemoryInfo     = psapi.NewProc("GetProcessMemoryInfo")
)

type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriorityClass   int32
	Flags           uint32
	ExeFile         [maxPath]uint16
}

type processMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func inspectProcesses(roots []int) (processReport, error) {
	snapshot, _, callErr := createToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snapshot == ^uintptr(0) {
		return processReport{}, fmt.Errorf("CreateToolhelp32Snapshot: %w", callErr)
	}
	defer closeHandle.Call(snapshot)
	entries := make(map[int]processEntry)
	entry := processEntry32{Size: uint32(unsafe.Sizeof(processEntry32{}))}
	first, _, firstErr := process32FirstW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if first == 0 {
		return processReport{}, fmt.Errorf("Process32FirstW: %w", firstErr)
	}
	for {
		pid := int(entry.ProcessID)
		entries[pid] = processEntry{record: processRecord{
			PID: pid, ParentPID: int(entry.ParentProcessID), Name: syscall.UTF16ToString(entry.ExeFile[:]), RSSBytes: processRSS(pid),
		}}
		next, _, _ := process32NextW.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if next == 0 {
			break
		}
	}
	return treeReport(roots, entries), nil
}

func processRSS(pid int) uint64 {
	handle, _, _ := openProcess.Call(processQueryLimitedInformation|processVMRead, 0, uintptr(pid))
	if handle == 0 {
		return 0
	}
	defer closeHandle.Call(handle)
	counters := processMemoryCounters{Size: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	if ok, _, _ := getProcessMemoryInfo.Call(handle, uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size)); ok == 0 {
		return 0
	}
	return uint64(counters.WorkingSetSize)
}
