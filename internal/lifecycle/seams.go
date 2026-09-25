package lifecycle

import (
	"context"

	"github.com/xenoviz/ruk/internal/state"
)

// Locker serializes one callback under a directory lock. Acquisition holds the
// per-workspace lock for the whole handoff, until the final lifecycle
// transition is published; release takes the same lock so it serializes with
// acquisition; warm, acquisition selection, and collection share one
// pool-maintenance lock path.
type Locker interface {
	With(context.Context, string, func() error) error
}

// StateReader supplies a read-only state snapshot. Callers use it to select
// or revalidate candidates without taking the state writer lock or rewriting
// the state file before the relevant workspace or maintenance lock is held.
// A state.Store satisfies this interface.
type StateReader interface {
	Read(context.Context) (*state.State, error)
}
