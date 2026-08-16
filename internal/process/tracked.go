package process

import (
	"context"
	"errors"
	"fmt"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

// IdentityUnavailableError means Ruk cannot prove a tracked process tree has
// finished. Callers must retain workspace ownership and retry later.
type IdentityUnavailableError struct {
	PID   int
	Cause error
}

// UnverifiedIdentityMarker is reserved for a durable sentinel written after a
// spawned process could not be described. It is never an identity to match or
// a basis for signaling; it only fences release until the PID/group boundary
// is absent.
const UnverifiedIdentityMarker = "ruk:unverified-process"

func IsUnverifiedRecord(record state.TrackedProcessRecord) bool {
	return record.StartedAt == UnverifiedIdentityMarker
}

func (err *IdentityUnavailableError) Error() string {
	return fmt.Sprintf("process %d could not be identified, so its workspace cannot be released safely", err.PID)
}

func (err *IdentityUnavailableError) Unwrap() error {
	return err.Cause
}

// Tracker checks durable process records without trusting a reusable numeric
// process identifier by itself.
type Tracker struct {
	Probe            lock.ProcessProbe
	DescendantsExist func(context.Context, int) (bool, error)
}

// NewTracker composes Ruk's native identity and descendant probes. On Windows
// this path uses kernel APIs directly and never launches PowerShell.
func NewTracker() Tracker {
	descendants := DescendantInspector{Table: NativeTable{}}
	return Tracker{
		Probe:            Inspector{},
		DescendantsExist: descendants.Exists,
	}
}

// Exists reports whether record still owns a live process tree. Unknown
// identity or descendant state returns IdentityUnavailableError instead of
// incorrectly authorizing workspace release.
func (tracker Tracker) Exists(ctx context.Context, record state.TrackedProcessRecord) (bool, error) {
	pid := int(record.PID)
	if record.PID <= 0 || int64(pid) != record.PID || record.StartedAt == "" {
		return false, &IdentityUnavailableError{PID: pid, Cause: errors.New("invalid tracked process record")}
	}
	if IsUnverifiedRecord(record) {
		if tracker.Probe == nil {
			return false, &IdentityUnavailableError{PID: pid, Cause: errors.New("process identity probe is unavailable")}
		}
		observed, err := tracker.Probe.Inspect(ctx, pid)
		if err != nil {
			return false, &IdentityUnavailableError{PID: pid, Cause: err}
		}
		if observed.Alive {
			return true, nil
		}
		if tracker.DescendantsExist == nil {
			return false, &IdentityUnavailableError{PID: pid, Cause: errors.New("process descendant probe is unavailable")}
		}
		descendants, err := tracker.DescendantsExist(ctx, pid)
		if err != nil {
			return false, &IdentityUnavailableError{PID: pid, Cause: err}
		}
		return descendants, nil
	}
	if tracker.Probe == nil {
		return false, &IdentityUnavailableError{PID: pid, Cause: errors.New("process identity probe is unavailable")}
	}
	observed, err := tracker.Probe.Inspect(ctx, pid)
	if err != nil {
		return false, &IdentityUnavailableError{PID: pid, Cause: err}
	}
	if observed.Alive {
		if !observed.IdentityKnown || observed.Identity == "" {
			return false, &IdentityUnavailableError{PID: pid, Cause: errors.New("process identity is unavailable")}
		}
		if observed.Identity == record.StartedAt {
			return true, nil
		}
	}

	if tracker.DescendantsExist == nil {
		return false, &IdentityUnavailableError{PID: pid, Cause: errors.New("process descendant probe is unavailable")}
	}
	descendants, err := tracker.DescendantsExist(ctx, pid)
	if err != nil {
		return false, &IdentityUnavailableError{PID: pid, Cause: err}
	}
	if descendants {
		return false, &IdentityUnavailableError{
			PID:   pid,
			Cause: errors.New("tracked leader is missing or reused while descendants remain"),
		}
	}
	return false, nil
}
