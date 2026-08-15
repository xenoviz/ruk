//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

// NativeForwardGroupSignaler forwards the original POSIX signal to a process
// group using syscall.Kill. It never starts a shell or helper process.
type NativeForwardGroupSignaler struct{}

// SignalGroupSignal forwards SIGINT or SIGTERM to the exact negative group
// identifier after refusing the supervisor's own group.
func (NativeForwardGroupSignaler) SignalGroupSignal(ctx context.Context, group int, signal os.Signal) error {
	if group <= 0 {
		return errors.New("process group must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !supportedForwardSignal(signal) {
		return errors.New("only SIGINT and SIGTERM may be forwarded")
	}
	ownGroup, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		return fmt.Errorf("inspect supervisor process group: %w", err)
	}
	if group == ownGroup {
		return fmt.Errorf("refusing to signal supervisor process group %d", group)
	}
	if err := syscall.Kill(-group, signal.(syscall.Signal)); err != nil {
		return fmt.Errorf("forward signal to process group %d: %w", group, err)
	}
	return nil
}

func defaultForwardGroupSignal() GroupSignalForwarder { return NativeForwardGroupSignaler{} }

func defaultForwardTreeSignal(_ lock.ProcessProbe, _ ProcessTable) TreeSignalForwarder {
	return nil
}

func forwardNativeSignal(ctx context.Context, forwarder NativeSignalForwarder, record state.TrackedProcessRecord, signal os.Signal) error {
	if record.GroupID == nil {
		return processUnavailable(int(record.PID), errors.New("managed signal forwarding requires a recorded process group"))
	}
	group := int(*record.GroupID)
	if *record.GroupID <= 0 || int64(group) != *record.GroupID {
		return processUnavailable(int(record.PID), errors.New("invalid recorded process group"))
	}
	if forwarder.probe == nil || forwarder.table == nil || forwarder.group == nil {
		return processUnavailable(int(record.PID), errors.New("signal forwarding dependency is unavailable"))
	}
	observed, err := forwarder.probe.Inspect(ctx, int(record.PID))
	if err != nil {
		return processUnavailable(int(record.PID), err)
	}
	entries, err := forwarder.table.Snapshot(ctx)
	if err != nil {
		return processUnavailable(int(record.PID), err)
	}
	if !observed.Alive {
		if groupHasMember(entries, group) {
			return processUnavailable(int(record.PID), errors.New("tracked process group remains after its leader exited"))
		}
		return nil
	}
	if !observed.IdentityKnown || observed.Identity == "" {
		return processUnavailable(int(record.PID), errors.New("process identity is unavailable"))
	}
	if observed.Identity != record.StartedAt || !groupHasLeader(entries, int(record.PID), group) {
		return processUnavailable(int(record.PID), errors.New("tracked leader identity or group membership changed"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	revalidated, err := forwarder.probe.Inspect(ctx, int(record.PID))
	if err != nil {
		return processUnavailable(int(record.PID), err)
	}
	if !revalidated.Alive || !revalidated.IdentityKnown || revalidated.Identity != record.StartedAt {
		return processUnavailable(int(record.PID), errors.New("process identity changed before signal forwarding"))
	}
	if err := forwarder.group.SignalGroupSignal(ctx, group, signal); err != nil {
		return fmt.Errorf("forward signal to process group %d: %w", group, err)
	}
	return nil
}
