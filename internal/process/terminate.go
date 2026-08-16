package process

import (
	"context"
	"errors"
	"fmt"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

// SignalKind is the portable cleanup intent passed to a platform signaler.
type SignalKind uint8

const (
	// SignalGraceful requests an orderly process-group shutdown.
	SignalGraceful SignalKind = iota
	// SignalForce requests immediate process-group termination.
	SignalForce
)

// GroupSignaler signals one process group through platform-native APIs.
type GroupSignaler interface {
	SignalGroup(context.Context, int, SignalKind) error
}

// GroupTerminator applies identity and membership fences before signaling a
// detached process group.
type GroupTerminator struct {
	Probe    lock.ProcessProbe
	Table    ProcessTable
	Signaler GroupSignaler
}

// TerminateGroup safely signals the detached group represented by record.
func (terminator GroupTerminator) TerminateGroup(ctx context.Context, record state.TrackedProcessRecord, force bool) (bool, error) {
	pid := int(record.PID)
	if IsUnverifiedRecord(record) {
		return false, processUnavailable(pid, errors.New("unverified process boundary cannot be signaled"))
	}
	if record.PID <= 0 || int64(pid) != record.PID || record.StartedAt == "" || record.GroupID == nil {
		return false, processUnavailable(pid, errors.New("invalid detached process record"))
	}
	group := int(*record.GroupID)
	if *record.GroupID <= 0 || int64(group) != *record.GroupID {
		return false, processUnavailable(pid, errors.New("invalid process group"))
	}
	if terminator.Probe == nil || terminator.Table == nil || terminator.Signaler == nil {
		return false, processUnavailable(pid, errors.New("process termination dependency is unavailable"))
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	observed, err := terminator.Probe.Inspect(ctx, pid)
	if err != nil {
		return false, processUnavailable(pid, err)
	}
	if observed.Alive && !observed.IdentityKnown {
		return false, processUnavailable(pid, errors.New("process identity is unavailable"))
	}

	entries, err := terminator.Table.Snapshot(ctx)
	if err != nil {
		return false, processUnavailable(pid, err)
	}
	if !observed.Alive || observed.Identity != record.StartedAt {
		if groupHasMember(entries, group) {
			return false, processUnavailable(pid, errors.New("tracked leader is missing or reused while its process group remains"))
		}
		return false, nil
	}
	if !groupHasLeader(entries, pid, group) {
		return false, processUnavailable(pid, errors.New("tracked leader is not a member of its recorded process group"))
	}

	if err := ctx.Err(); err != nil {
		return false, err
	}
	revalidated, err := terminator.Probe.Inspect(ctx, pid)
	if err != nil {
		return false, processUnavailable(pid, err)
	}
	if !revalidated.Alive || !revalidated.IdentityKnown || revalidated.Identity != record.StartedAt {
		return false, processUnavailable(pid, errors.New("process identity changed before signaling"))
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	kind := SignalGraceful
	if force {
		kind = SignalForce
	}
	if err := terminator.Signaler.SignalGroup(ctx, group, kind); err != nil {
		return false, fmt.Errorf("signal process group %d: %w", group, err)
	}
	return true, nil
}

func groupHasMember(entries []Entry, group int) bool {
	for _, entry := range entries {
		if entry.PID > 0 && entry.GroupID == group {
			return true
		}
	}
	return false
}

func groupHasLeader(entries []Entry, pid, group int) bool {
	for _, entry := range entries {
		if entry.PID == pid && entry.GroupID == group {
			return true
		}
	}
	return false
}

func processUnavailable(pid int, cause error) *IdentityUnavailableError {
	return &IdentityUnavailableError{PID: pid, Cause: cause}
}
