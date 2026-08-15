//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/xenoviz/ruk/internal/state"
)

// NativePIDSignaler sends POSIX signals directly to one exact PID.
type NativePIDSignaler struct{}

// SignalPID maps graceful and force cleanup to SIGTERM and SIGKILL without a
// helper subprocess. The supervisor itself is never a valid cleanup target.
func (NativePIDSignaler) SignalPID(ctx context.Context, pid int, kind SignalKind) error {
	if pid <= 0 {
		return errors.New("process ID must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if pid == os.Getpid() {
		return fmt.Errorf("refusing to signal supervisor process %d", pid)
	}
	signal := syscall.SIGTERM
	switch kind {
	case SignalGraceful:
		signal = syscall.SIGTERM
	case SignalForce:
		signal = syscall.SIGKILL
	default:
		return fmt.Errorf("unsupported PID signal kind %d", kind)
	}
	if err := syscall.Kill(pid, signal); err != nil {
		return fmt.Errorf("signal process %d: %w", pid, err)
	}
	return nil
}

func defaultPIDSignaler() PIDSignaler { return NativePIDSignaler{} }

func terminateNativeRecord(ctx context.Context, manager NativeProcessManager, record state.TrackedProcessRecord, force bool) (bool, error) {
	if record.GroupID != nil {
		terminator := GroupTerminator{
			Probe:    manager.probe,
			Table:    manager.table,
			Signaler: manager.groupSignaler,
		}
		return terminator.TerminateGroup(ctx, record, force)
	}
	if manager.probe == nil {
		return false, processUnavailable(int(record.PID), errors.New("process identity probe is unavailable"))
	}
	if manager.pidSignaler == nil {
		return false, processUnavailable(int(record.PID), errors.New("PID signaler is unavailable"))
	}
	observed, err := manager.probe.Inspect(ctx, int(record.PID))
	if err != nil {
		return false, processUnavailable(int(record.PID), err)
	}
	if !observed.Alive {
		return false, nil
	}
	if !observed.IdentityKnown || observed.Identity == "" {
		return false, processUnavailable(int(record.PID), errors.New("process identity is unavailable"))
	}
	if observed.Identity != record.StartedAt {
		return false, processUnavailable(int(record.PID), errors.New("process identity changed before signaling"))
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	kind := SignalGraceful
	if force {
		kind = SignalForce
	}
	if err := manager.pidSignaler.SignalPID(ctx, int(record.PID), kind); err != nil {
		return false, fmt.Errorf("signal process %d: %w", record.PID, err)
	}
	return true, nil
}
