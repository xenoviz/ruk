//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"syscall"
	"unsafe"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

func defaultPIDSignaler() PIDSignaler { return windowsPIDSignaler{} }

// windowsPIDSignaler exists for the shared manager seam. Windows release uses
// windowsTreeTerminator instead so descendants cannot be orphaned.
type windowsPIDSignaler struct{}

func (windowsPIDSignaler) SignalPID(context.Context, int, SignalKind) error {
	return errors.New("Windows release requires native process-tree termination")
}

func terminateNativeRecord(ctx context.Context, manager NativeProcessManager, record state.TrackedProcessRecord, force bool) (bool, error) {
	terminator := manager.treeTerminator
	if terminator == nil {
		terminator = windowsTreeTerminator{probe: manager.probe, table: manager.table}
	}
	return terminator.TerminateTree(ctx, record, force)
}

type windowsTreeTerminator struct {
	probe lock.ProcessProbe
	table ProcessTable
}

func (terminator windowsTreeTerminator) TerminateTree(ctx context.Context, record state.TrackedProcessRecord, force bool) (bool, error) {
	if terminator.probe == nil || terminator.table == nil {
		return false, processUnavailable(int(record.PID), errors.New("Windows process-tree dependency is unavailable"))
	}
	leader, err := terminator.probe.Inspect(ctx, int(record.PID))
	if err != nil {
		return false, processUnavailable(int(record.PID), err)
	}
	if leader.Alive && (!leader.IdentityKnown || leader.Identity == "") {
		return false, processUnavailable(int(record.PID), errors.New("process identity is unavailable"))
	}
	entries, err := terminator.table.Snapshot(ctx)
	if err != nil {
		return false, processUnavailable(int(record.PID), err)
	}
	rootPresent := processTableHasPID(entries, int(record.PID))
	pids := descendantPIDs(entries, int(record.PID))
	if !leader.Alive {
		if len(pids) > 0 {
			return false, processUnavailable(int(record.PID), errors.New("tracked leader is missing while descendants remain"))
		}
		return false, nil
	}
	if leader.Identity != record.StartedAt {
		return false, processUnavailable(int(record.PID), errors.New("process identity changed before signaling"))
	}
	if !rootPresent || len(pids) == 0 {
		return false, processUnavailable(int(record.PID), errors.New("tracked process is absent from the native process snapshot"))
	}

	identities := make(map[int]string, len(pids))
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		observed, inspectErr := terminator.probe.Inspect(ctx, pid)
		if inspectErr != nil || !observed.Alive || !observed.IdentityKnown || observed.Identity == "" {
			if inspectErr == nil {
				inspectErr = errors.New("process identity is unavailable")
			}
			return false, processUnavailable(pid, inspectErr)
		}
		if pid == int(record.PID) && observed.Identity != record.StartedAt {
			return false, processUnavailable(pid, errors.New("process identity changed before signaling"))
		}
		identities[pid] = observed.Identity
	}
	// Descendants are terminated first; the recorded leader is the final
	// signal, preventing a surviving child from being detached by its parent.
	depths := processDepths(entries, int(record.PID))
	sort.SliceStable(pids, func(left, right int) bool {
		return depths[pids[left]] > depths[pids[right]]
	})
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		handle, identity, openErr := openWindowsTrackedProcess(pid)
		if openErr != nil {
			return false, processUnavailable(pid, openErr)
		}
		if identity != identities[pid] {
			closeErr := closeWindowsHandle(handle)
			identityErr := errors.New("process identity changed before termination")
			if closeErr != nil {
				identityErr = errors.Join(identityErr, closeErr)
			}
			return false, processUnavailable(pid, identityErr)
		}
		terminateErr := terminateWindowsHandle(handle)
		closeErr := closeWindowsHandle(handle)
		if terminateErr != nil {
			return false, terminateErr
		}
		if closeErr != nil {
			return false, closeErr
		}
	}
	_ = force // Windows has no portable graceful signal; TerminateProcess is native and tree-fenced.
	return true, nil
}

func descendantPIDs(entries []Entry, root int) []int {
	if !processTableHasPID(entries, root) {
		return nil
	}
	children := make(map[int][]int)
	for _, entry := range entries {
		if entry.PID > 0 && entry.ParentPID > 0 {
			children[entry.ParentPID] = append(children[entry.ParentPID], entry.PID)
		}
	}
	seen := map[int]bool{root: true}
	result := make([]int, 0)
	queue := []int{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			result = append(result, child)
			queue = append(queue, child)
		}
	}
	result = append(result, root)
	return result
}

func processTableHasPID(entries []Entry, pid int) bool {
	for _, entry := range entries {
		if entry.PID == pid {
			return true
		}
	}
	return false
}

func processDepths(entries []Entry, root int) map[int]int {
	parents := make(map[int]int, len(entries))
	for _, entry := range entries {
		if entry.PID > 0 {
			parents[entry.PID] = entry.ParentPID
		}
	}
	depths := map[int]int{root: 0}
	for pid := range parents {
		if pid == root {
			continue
		}
		seen := map[int]bool{pid: true}
		current := pid
		depth := 0
		for current != root {
			parent, ok := parents[current]
			if !ok || seen[parent] {
				depth = 0
				break
			}
			seen[parent] = true
			current = parent
			depth++
		}
		if current == root {
			depths[pid] = depth
		}
	}
	return depths
}

func openWindowsTrackedProcess(pid int) (syscall.Handle, string, error) {
	if pid <= 0 {
		return 0, "", errors.New("process ID must be positive")
	}
	handle, _, callErr := openProcessForJob.Call(uintptr(processTerminate|processQueryLimitedInformationAccess), 0, uintptr(pid))
	if handle == 0 {
		return 0, "", windowsCallError(callErr)
	}
	processHandle := syscall.Handle(handle)
	identity, err := windowsHandleIdentity(processHandle)
	if err != nil {
		_ = closeWindowsHandle(processHandle)
		return 0, "", err
	}
	return processHandle, identity, nil
}

func windowsHandleIdentity(handle syscall.Handle) (string, error) {
	var created, exited, kernel, user filetime
	ok, _, callErr := getProcessTimes.Call(uintptr(handle), uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if ok == 0 {
		return "", windowsCallError(callErr)
	}
	raw := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if raw > ^uint64(0)-dotNetEpochOffset {
		return "", errors.New("process creation identity overflows")
	}
	return fmt.Sprintf("%d", dotNetTicks(raw)), nil
}

func terminateWindowsHandle(handle syscall.Handle) error {
	ok, _, callErr := terminateProcess.Call(uintptr(handle), 1)
	if ok == 0 {
		return fmt.Errorf("terminate Windows process: %w", windowsCallError(callErr))
	}
	return nil
}

func closeWindowsHandle(handle syscall.Handle) error {
	ok, _, callErr := closeHandle.Call(uintptr(handle))
	if ok == 0 {
		return fmt.Errorf("close Windows process handle: %w", windowsCallError(callErr))
	}
	return nil
}
