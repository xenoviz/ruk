package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

// NativeProcessDescriber uses the existing native identity and process table
// seams. POSIX detached children must be their own process-group leaders.
type NativeProcessDescriber struct {
	Probe lock.ProcessProbe
	Table ProcessTable
}

func (describer NativeProcessDescriber) Describe(ctx context.Context, pid int, mode ProcessMode, command []string) (state.TrackedProcessRecord, error) {
	if describer.Probe == nil {
		return state.TrackedProcessRecord{}, errors.New("process: identity probe is unavailable")
	}
	observed, err := describer.Probe.Inspect(ctx, pid)
	if err != nil {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: err}
	}
	if !observed.Alive || !observed.IdentityKnown || observed.Identity == "" {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: errors.New("process identity is unavailable")}
	}
	record := state.TrackedProcessRecord{PID: int64(pid), Command: append([]string(nil), command...), StartedAt: observed.Identity}
	if mode != Detached || runtime.GOOS == "windows" {
		return record, nil
	}
	if describer.Table == nil {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: errors.New("process table is unavailable")}
	}
	entries, err := describer.Table.Snapshot(ctx)
	if err != nil {
		return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: err}
	}
	for _, entry := range entries {
		if entry.PID == pid && entry.GroupID == pid {
			group := int64(pid)
			record.GroupID = &group
			return record, nil
		}
	}
	return state.TrackedProcessRecord{}, &IdentityUnavailableError{PID: pid, Cause: errors.New("detached child is not its own process-group leader")}
}

// NativeProcessCleaner identity-checks an attached child before signaling it,
// and delegates detached cleanup to the existing identity-fenced terminator.
type NativeProcessCleaner struct {
	Probe    lock.ProcessProbe
	Table    ProcessTable
	Signaler GroupSignaler
}

func (cleaner NativeProcessCleaner) Cleanup(ctx context.Context, child Child, record state.TrackedProcessRecord) error {
	if cleaner.Probe == nil || child == nil {
		return &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("process cleanup dependency is unavailable")}
	}
	if IsUnverifiedRecord(record) {
		return &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("unverified process boundary cannot be signaled")}
	}
	if record.GroupID != nil {
		if cleaner.Signaler == nil {
			return &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("process-group signaler is unavailable")}
		}
		table := cleaner.Table
		if table == nil {
			table = NativeTable{}
		}
		terminated, err := (GroupTerminator{Probe: cleaner.Probe, Table: table, Signaler: cleaner.Signaler}).TerminateGroup(ctx, record, true)
		if err != nil {
			return err
		}
		if !terminated {
			return nil
		}
		return nil
	}
	observed, err := cleaner.Probe.Inspect(ctx, int(record.PID))
	if err != nil || !observed.Alive || !observed.IdentityKnown || !exactIdentityMatch(record.StartedAt, observed.Identity) {
		if err == nil {
			err = errors.New("process identity changed before registration cleanup")
		}
		return &IdentityUnavailableError{PID: int(record.PID), Cause: err}
	}
	// Recheck immediately before signaling: the first probe can race PID reuse.
	revalidated, err := cleaner.Probe.Inspect(ctx, int(record.PID))
	if err != nil || !revalidated.Alive || !revalidated.IdentityKnown || !exactIdentityMatch(record.StartedAt, revalidated.Identity) {
		if err == nil {
			err = errors.New("process identity changed before registration cleanup signal")
		}
		return &IdentityUnavailableError{PID: int(record.PID), Cause: err}
	}
	if err := child.Signal(os.Kill); err != nil {
		return fmt.Errorf("terminate process %d: %w", record.PID, err)
	}
	return nil
}

// CleanupUnknown uses an exact child-owned boundary only where the platform
// provides one. POSIX detached children require a verified group identity and
// therefore remain retained when description failed.
func (cleaner NativeProcessCleaner) CleanupUnknown(ctx context.Context, child Child, mode ProcessMode, record state.TrackedProcessRecord) (bool, error) {
	if child == nil {
		return false, errors.New("process: unknown-child cleanup dependency is unavailable")
	}
	if runtime.GOOS == "windows" {
		if err := child.Signal(os.Kill); err != nil {
			return false, fmt.Errorf("terminate unknown Windows child boundary: %w", err)
		}
		return true, nil
	}
	if mode == Attached {
		if err := child.Signal(os.Kill); err != nil {
			return false, fmt.Errorf("terminate unknown attached child: %w", err)
		}
		// Killing the exact leader does not prove that an attached child did
		// not create descendants in the supervisor's process group.
		return false, nil
	}
	return false, &IdentityUnavailableError{PID: int(record.PID), Cause: errors.New("detached process group identity is unavailable")}
}

// Exists implements ProcessCleanupVerifier for native registration cleanup.
func (cleaner NativeProcessCleaner) Exists(ctx context.Context, record state.TrackedProcessRecord) (bool, error) {
	manager := NewNativeProcessManager(ReleaseManagerOptions{Probe: cleaner.Probe, Table: cleaner.Table})
	return manager.Exists(ctx, record)
}
