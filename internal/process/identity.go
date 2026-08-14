// Package process provides process identity and supervision primitives without
// shelling out to PowerShell on Windows.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/xenoviz/ruk/internal/lock"
)

const dotNetEpochOffset = uint64(504_911_232_000_000_000)

// Inspector implements lock.ProcessProbe with platform process APIs.
type Inspector struct{}

var _ lock.ProcessProbe = Inspector{}

// Inspect returns liveness and the strongest compatible start-time identity
// available for pid. Invalid process identifiers are confirmed missing.
func (Inspector) Inspect(ctx context.Context, pid int) (lock.ProcessState, error) {
	if pid <= 0 {
		return lock.ProcessState{}, nil
	}
	if err := ctx.Err(); err != nil {
		return lock.ProcessState{}, err
	}
	return inspectPlatform(ctx, pid)
}

// CurrentIdentity returns the identity persisted for the current Ruk process.
func CurrentIdentity(ctx context.Context) (string, error) {
	state, err := (Inspector{}).Inspect(ctx, os.Getpid())
	if err != nil {
		return "", err
	}
	if !state.Alive || !state.IdentityKnown || state.Identity == "" {
		return "", errors.New("process: current process identity is unavailable")
	}
	return state.Identity, nil
}

func dotNetTicks(filetime uint64) uint64 {
	return filetime + dotNetEpochOffset
}

func formatPOSIXIdentity(started time.Time) string {
	return started.In(time.Local).Format("Mon Jan _2 15:04:05 2006")
}

func unavailableIdentity(pid int, err error) (lock.ProcessState, error) {
	return lock.ProcessState{Alive: true, IdentityKnown: false}, fmt.Errorf("inspect process %d identity: %w", pid, err)
}
