//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// NativeGroupSignaler signals detached POSIX process groups without helper
// subprocesses.
type NativeGroupSignaler struct{}

// SignalGroup maps the portable cleanup intent to a POSIX group signal.
func (NativeGroupSignaler) SignalGroup(ctx context.Context, group int, kind SignalKind) error {
	if group <= 0 {
		return errors.New("process group must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ownGroup, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		return fmt.Errorf("inspect supervisor process group: %w", err)
	}
	if group == ownGroup {
		return fmt.Errorf("refusing to signal supervisor process group %d", group)
	}
	var signal syscall.Signal
	switch kind {
	case SignalGraceful:
		signal = syscall.SIGTERM
	case SignalForce:
		signal = syscall.SIGKILL
	default:
		return fmt.Errorf("unsupported process-group signal kind %d", kind)
	}
	if err := syscall.Kill(-group, signal); err != nil {
		return fmt.Errorf("signal process group %d: %w", group, err)
	}
	return nil
}
