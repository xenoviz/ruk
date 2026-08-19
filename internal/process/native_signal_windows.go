//go:build windows

package process

import (
	"context"
	"errors"
	"os"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

func defaultForwardGroupSignal() GroupSignalForwarder { return nil }

// nativeWindowsTreeSignalForwarder uses the existing native process-tree
// manager, which snapshots descendants, verifies identities, and terminates
// only handles whose creation identity still matches. No console helper or
// taskkill fallback is permitted.
type nativeWindowsTreeSignalForwarder struct {
	manager NativeProcessManager
}

func (forwarder nativeWindowsTreeSignalForwarder) ForwardTree(ctx context.Context, record state.TrackedProcessRecord, signal os.Signal) error {
	if !supportedForwardSignal(signal) {
		return errors.New("process: only SIGINT and SIGTERM may be forwarded")
	}
	terminated, err := forwarder.manager.Terminate(ctx, record, false)
	if err != nil {
		return err
	}
	if !terminated {
		return nil
	}
	return nil
}

func defaultForwardTreeSignal(probe lock.ProcessProbe, table ProcessTable) TreeSignalForwarder {
	return nativeWindowsTreeSignalForwarder{manager: NewNativeProcessManager(ReleaseManagerOptions{Probe: probe, Table: table})}
}

func forwardNativeSignal(ctx context.Context, forwarder NativeSignalForwarder, record state.TrackedProcessRecord, signal os.Signal) error {
	if forwarder.tree == nil {
		return processUnavailable(int(record.PID), errors.New("native Windows tree signaler is unavailable"))
	}
	return forwarder.tree.ForwardTree(ctx, record, signal)
}
