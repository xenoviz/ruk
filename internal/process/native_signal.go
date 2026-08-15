package process

import (
	"context"
	"errors"
	"os"
	"syscall"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

// GroupSignalForwarder is the native boundary for forwarding an interrupt to
// one recorded process group. It carries the original signal rather than
// reducing SIGINT to a termination request.
type GroupSignalForwarder interface {
	SignalGroupSignal(context.Context, int, os.Signal) error
}

// TreeSignalForwarder is the native Windows boundary for forwarding a signal
// to a recorded process tree. Implementations must retain the tree when its
// identity cannot be proved.
type TreeSignalForwarder interface {
	ForwardTree(context.Context, state.TrackedProcessRecord, os.Signal) error
}

// SignalForwarderOptions configures native signal forwarding. Nil seams select
// the platform-native implementations; injected seams make identity and
// no-helper-process behavior deterministic in tests.
type SignalForwarderOptions struct {
	Probe       lock.ProcessProbe
	Table       ProcessTable
	GroupSignal GroupSignalForwarder
	TreeSignal  TreeSignalForwarder
}

// NativeSignalForwarder forwards SIGINT and SIGTERM for managed detached
// commands after revalidating the recorded leader identity and membership.
type NativeSignalForwarder struct {
	probe lock.ProcessProbe
	table ProcessTable
	group GroupSignalForwarder
	tree  TreeSignalForwarder
}

var _ SignalForwarder = NativeSignalForwarder{}

// NewNativeSignalForwarder constructs the production signal-forwarding hook.
// An optional options value supplies deterministic probe, table, and native
// signaling seams without changing the public SignalForwarder contract.
func NewNativeSignalForwarder(values ...SignalForwarderOptions) NativeSignalForwarder {
	var options SignalForwarderOptions
	if len(values) != 0 {
		options = values[0]
	}
	probe := options.Probe
	if probe == nil {
		probe = Inspector{}
	}
	table := options.Table
	if table == nil {
		table = NativeTable{}
	}
	group := options.GroupSignal
	if group == nil {
		group = defaultForwardGroupSignal()
	}
	tree := options.TreeSignal
	if tree == nil {
		tree = defaultForwardTreeSignal(probe, table)
	}
	return NativeSignalForwarder{probe: probe, table: table, group: group, tree: tree}
}

// NewSignalForwarder is a concise constructor alias for runtime defaults.
func NewSignalForwarder(values ...SignalForwarderOptions) NativeSignalForwarder {
	return NewNativeSignalForwarder(values...)
}

// Forward validates the signal, then delegates to the platform implementation
// which performs an immediate identity and group/tree fence before signaling.
func (forwarder NativeSignalForwarder) Forward(ctx context.Context, record state.TrackedProcessRecord, signal os.Signal) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !supportedForwardSignal(signal) {
		return errors.New("process: only SIGINT and SIGTERM may be forwarded")
	}
	if record.PID <= 0 || int64(int(record.PID)) != record.PID || record.StartedAt == "" {
		return processUnavailable(int(record.PID), errors.New("invalid tracked process record"))
	}
	return forwardNativeSignal(ctx, forwarder, record, signal)
}

func supportedForwardSignal(signal os.Signal) bool {
	return signal == syscall.SIGINT || signal == syscall.SIGTERM
}
