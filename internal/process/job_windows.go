//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

const (
	processTerminate                     = uint32(0x0001)
	processSetQuota                      = uint32(0x0100)
	processQueryLimitedInformationAccess = uint32(0x1000)
	createSuspended                      = uint32(0x00000004)
	jobObjectExtendedLimitInformation    = uint32(9)
	jobObjectLimitKillOnJobClose         = uint32(0x2000)

	threadSuspendResume = uint32(0x0002)
	threadSnapshot      = uintptr(0x00000004)
	waitObjectSignaled  = uint32(0x00000000)
	waitObjectTimeout   = uint32(0x00000102)
	waitObjectFailed    = ^uint32(0)

	terminateWaitMS = uint32(100)
)

var (
	createJobObjectW    = kernel32.NewProc("CreateJobObjectW")
	setInformationJob   = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJob  = kernel32.NewProc("AssignProcessToJobObject")
	terminateJobObject  = kernel32.NewProc("TerminateJobObject")
	openProcessForJob   = kernel32.NewProc("OpenProcess")
	terminateProcess    = kernel32.NewProc("TerminateProcess")
	waitForSingleObject = kernel32.NewProc("WaitForSingleObject")
	openThread          = kernel32.NewProc("OpenThread")
	resumeThread        = kernel32.NewProc("ResumeThread")
	thread32First       = kernel32.NewProc("Thread32First")
	thread32Next        = kernel32.NewProc("Thread32Next")
)

type windowsThreadEntry32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePriority   int32
	DeltaPriority  int32
	Flags          uint32
}

type windowsJobBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type windowsJobIOCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type windowsJobExtendedLimitInformation struct {
	BasicLimitInformation windowsJobBasicLimitInformation
	IoInfo                windowsJobIOCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// windowsJobSystem is injectable so lifecycle tests can exercise creation,
// assignment, termination, and close ordering without requiring Windows
// process state. Production code uses nativeWindowsJobSystem.
type windowsJobSystem struct {
	create           func() (syscall.Handle, error)
	setLimits        func(syscall.Handle) error
	openProcess      func(uint32, uint32) (syscall.Handle, error)
	assign           func(syscall.Handle, syscall.Handle) error
	terminate        func(syscall.Handle) error
	terminateProcess func(syscall.Handle) error
	wait             func(syscall.Handle, uint32) (uint32, error)
	close            func(syscall.Handle) error
}

var nativeWindowsJobSystem = windowsJobSystem{
	create: func() (syscall.Handle, error) {
		handle, _, callErr := createJobObjectW.Call(0, 0)
		if handle == 0 {
			return 0, windowsCallError(callErr)
		}
		return syscall.Handle(handle), nil
	},
	setLimits: func(handle syscall.Handle) error {
		limits := windowsJobExtendedLimitInformation{}
		limits.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
		ok, _, callErr := setInformationJob.Call(
			uintptr(handle),
			uintptr(jobObjectExtendedLimitInformation),
			uintptr(unsafe.Pointer(&limits)),
			uintptr(unsafe.Sizeof(limits)),
		)
		if ok == 0 {
			return windowsCallError(callErr)
		}
		return nil
	},
	openProcess: func(access uint32, pid uint32) (syscall.Handle, error) {
		handle, _, callErr := openProcessForJob.Call(uintptr(access), 0, uintptr(pid))
		if handle == 0 {
			return 0, windowsCallError(callErr)
		}
		return syscall.Handle(handle), nil
	},
	assign: func(job, process syscall.Handle) error {
		ok, _, callErr := assignProcessToJob.Call(uintptr(job), uintptr(process))
		if ok == 0 {
			return windowsCallError(callErr)
		}
		return nil
	},
	terminate: func(job syscall.Handle) error {
		ok, _, callErr := terminateJobObject.Call(uintptr(job), 1)
		if ok == 0 {
			return windowsCallError(callErr)
		}
		return nil
	},
	terminateProcess: func(process syscall.Handle) error {
		ok, _, callErr := terminateProcess.Call(uintptr(process), 1)
		if ok == 0 {
			return windowsCallError(callErr)
		}
		return nil
	},
	wait: func(handle syscall.Handle, timeout uint32) (uint32, error) {
		result, _, callErr := waitForSingleObject.Call(uintptr(handle), uintptr(timeout))
		if uint32(result) == waitObjectFailed {
			return uint32(result), windowsCallError(callErr)
		}
		return uint32(result), nil
	},
	close: func(handle syscall.Handle) error {
		ok, _, callErr := closeHandle.Call(uintptr(handle))
		if ok == 0 {
			return windowsCallError(callErr)
		}
		return nil
	},
}

type windowsJob struct {
	mu     sync.Mutex
	handle syscall.Handle
	system windowsJobSystem
	closed bool
}

func newWindowsJob() (*windowsJob, error) {
	return newWindowsJobWith(nativeWindowsJobSystem)
}

func newWindowsJobWith(system windowsJobSystem) (*windowsJob, error) {
	if system.create == nil || system.setLimits == nil || system.openProcess == nil || system.assign == nil || system.terminate == nil || system.terminateProcess == nil || system.wait == nil || system.close == nil {
		return nil, errors.New("process: Windows Job Object boundary is incomplete")
	}
	handle, err := system.create()
	if err != nil {
		return nil, fmt.Errorf("create Windows Job Object: %w", err)
	}
	if handle == 0 {
		return nil, errors.New("create Windows Job Object: empty handle")
	}
	job := &windowsJob{handle: handle, system: system}
	if err := system.setLimits(handle); err != nil {
		_ = system.close(handle)
		return nil, fmt.Errorf("configure Windows Job Object: %w", err)
	}
	return job, nil
}

func (job *windowsJob) AssignProcess(pid int) error {
	if pid <= 0 {
		return errors.New("process: invalid Windows child PID")
	}
	job.mu.Lock()
	if job.closed {
		job.mu.Unlock()
		return errors.New("process: Windows Job Object is closed")
	}
	handle := job.handle
	system := job.system
	job.mu.Unlock()

	process, err := system.openProcess(processTerminate|processSetQuota|processQueryLimitedInformationAccess, uint32(pid))
	if err != nil {
		return fmt.Errorf("open Windows child %d for Job Object assignment: %w", pid, err)
	}
	defer func() { _ = system.close(process) }()
	if err := system.assign(handle, process); err != nil {
		return fmt.Errorf("assign Windows child %d to Job Object: %w", pid, err)
	}
	return nil
}

func (job *windowsJob) Terminate() error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.closed {
		return errors.New("process: Windows Job Object is closed")
	}
	if err := job.system.terminate(job.handle); err != nil {
		return fmt.Errorf("terminate Windows Job Object: %w", err)
	}
	return nil
}

func (job *windowsJob) Close() error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.closed {
		return nil
	}
	job.closed = true
	return job.system.close(job.handle)
}

// WaitEmpty waits until the Job Object has no active processes. A short
// timeout makes context cancellation observable while preserving the job
// handle and its kill-on-close ownership fence until descendants finish.
func (job *windowsJob) WaitEmpty(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	terminated := false
	for {
		if err := ctx.Err(); err != nil && !terminated {
			if terminateErr := job.Terminate(); terminateErr != nil {
				return fmt.Errorf("terminate Windows Job Object after context cancellation: %w", terminateErr)
			}
			terminated = true
		}
		job.mu.Lock()
		if job.closed {
			job.mu.Unlock()
			return errors.New("process: Windows Job Object is closed")
		}
		system := job.system
		handle := job.handle
		job.mu.Unlock()
		result, err := system.wait(handle, terminateWaitMS)
		if err != nil {
			return fmt.Errorf("wait for Windows Job Object processes: %w", err)
		}
		switch result {
		case waitObjectSignaled:
			return nil
		case waitObjectTimeout:
			continue
		default:
			return fmt.Errorf("wait for Windows Job Object processes: unexpected wait result 0x%x", result)
		}
	}
}

// resumeWindowsProcess resumes the primary thread of a process created with
// CREATE_SUSPENDED. A suspended process has not run user code and therefore
// has only its initial thread, making this native lookup race-free.
func resumeWindowsProcess(pid int) error {
	if pid <= 0 {
		return errors.New("process: invalid Windows child PID")
	}
	snapshot, _, callErr := createToolhelp32Snapshot.Call(threadSnapshot, 0)
	if snapshot == ^uintptr(0) {
		return fmt.Errorf("snapshot Windows threads: %w", windowsCallError(callErr))
	}
	defer func() { _, _, _ = closeHandle.Call(snapshot) }()

	entry := windowsThreadEntry32{Size: uint32(unsafe.Sizeof(windowsThreadEntry32{}))}
	ok, _, callErr := thread32First.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
	if ok == 0 {
		return fmt.Errorf("find suspended Windows child thread %d: %w", pid, windowsCallError(callErr))
	}
	for {
		if int(entry.OwnerProcessID) == pid {
			thread, _, openErr := openThread.Call(uintptr(threadSuspendResume), 0, uintptr(entry.ThreadID))
			if thread == 0 {
				return fmt.Errorf("open suspended Windows child thread %d: %w", pid, windowsCallError(openErr))
			}
			defer func() { _, _, _ = closeHandle.Call(thread) }()
			previous, _, resumeErr := resumeThread.Call(thread)
			if uint32(previous) == ^uint32(0) {
				return fmt.Errorf("resume suspended Windows child %d: %w", pid, windowsCallError(resumeErr))
			}
			if uint32(previous) == 0 {
				return fmt.Errorf("resume suspended Windows child %d: thread was not suspended", pid)
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(windowsThreadEntry32{}))
		ok, _, callErr = thread32Next.Call(snapshot, uintptr(unsafe.Pointer(&entry)))
		if ok == 0 {
			return fmt.Errorf("find suspended Windows child thread %d: %w", pid, windowsCallError(callErr))
		}
	}
}

func windowsCallError(callErr error) error {
	if errno, ok := callErr.(syscall.Errno); ok && errno == 0 {
		return syscall.EINVAL
	}
	if callErr == nil {
		return syscall.EINVAL
	}
	return callErr
}
