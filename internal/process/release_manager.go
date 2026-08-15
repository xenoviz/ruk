package process

import (
	"context"
	"errors"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

// PIDSignaler is the narrow native boundary for an attached process. It is
// intentionally separate from GroupSignaler so tests cannot accidentally
// shell out or signal an untracked process group.
type PIDSignaler interface {
	SignalPID(context.Context, int, SignalKind) error
}

// TreeTerminator is an optional platform tree boundary. Windows uses it to
// inject a snapshot/handle implementation in tests; POSIX uses GroupTerminator
// directly because process-group membership is the native ownership fence.
type TreeTerminator interface {
	TerminateTree(context.Context, state.TrackedProcessRecord, bool) (bool, error)
}

// ReleaseManagerOptions configures native process inspection and signaling.
// Zero values select the production native implementations.
type ReleaseManagerOptions struct {
	Probe          lock.ProcessProbe
	Table          ProcessTable
	GroupSignaler  GroupSignaler
	PIDSignaler    PIDSignaler
	TreeTerminator TreeTerminator
}

// ProcessManagerOptions is a compatibility name for ReleaseManagerOptions.
type ProcessManagerOptions = ReleaseManagerOptions

// NativeProcessManager implements the process seam consumed by lifecycle
// release. Every operation proves the recorded identity before reporting or
// signaling a process; an unknown identity is retained as an error.
type NativeProcessManager struct {
	probe          lock.ProcessProbe
	table          ProcessTable
	groupSignaler  GroupSignaler
	pidSignaler    PIDSignaler
	treeTerminator TreeTerminator
	tracker        Tracker
}

// ReleaseProcessManager is the release-oriented compatibility name.
type ReleaseProcessManager = NativeProcessManager

// NativeReleaseProcessManager is an explicit native-backend compatibility
// name for integrations that distinguish it from lifecycle's seam.
type NativeReleaseProcessManager = NativeProcessManager

var _ interface {
	Exists(context.Context, state.TrackedProcessRecord) (bool, error)
	Terminate(context.Context, state.TrackedProcessRecord, bool) (bool, error)
} = NativeProcessManager{}

// NewNativeProcessManager constructs a manager with native seams. An optional
// options value keeps the production call site concise while allowing tests to
// inject every process boundary.
func NewNativeProcessManager(values ...ReleaseManagerOptions) NativeProcessManager {
	var options ReleaseManagerOptions
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
	groupSignaler := options.GroupSignaler
	if groupSignaler == nil {
		groupSignaler = defaultGroupSignaler()
	}
	pidSignaler := options.PIDSignaler
	if pidSignaler == nil {
		pidSignaler = defaultPIDSignaler()
	}
	return NativeProcessManager{
		probe:          probe,
		table:          table,
		groupSignaler:  groupSignaler,
		pidSignaler:    pidSignaler,
		treeTerminator: options.TreeTerminator,
		tracker: Tracker{
			Probe:            probe,
			DescendantsExist: (DescendantInspector{Table: table}).Exists,
		},
	}
}

// NewReleaseManager is the release-oriented constructor name used by
// integrations that do not otherwise need to mention the native backend.
func NewReleaseManager(values ...ReleaseManagerOptions) NativeProcessManager {
	return NewNativeProcessManager(values...)
}

// NewReleaseProcessManager constructs the native release process manager.
func NewReleaseProcessManager(values ...ReleaseManagerOptions) NativeProcessManager {
	return NewNativeProcessManager(values...)
}

// Exists delegates to Tracker, which fails closed for unknown identity,
// reused PIDs, and leaderless descendant trees.
func (manager NativeProcessManager) Exists(ctx context.Context, record state.TrackedProcessRecord) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if manager.probe == nil || manager.tracker.Probe == nil {
		return false, processUnavailable(int(record.PID), errors.New("process identity probe is unavailable"))
	}
	if record.GroupID != nil {
		return manager.detachedGroupExists(ctx, record)
	}
	return manager.tracker.Exists(ctx, record)
}

func (manager NativeProcessManager) detachedGroupExists(ctx context.Context, record state.TrackedProcessRecord) (bool, error) {
	pid := int(record.PID)
	groupID := int(*record.GroupID)
	if record.PID <= 0 || int64(pid) != record.PID || *record.GroupID <= 0 || int64(groupID) != *record.GroupID || record.StartedAt == "" {
		return false, processUnavailable(pid, errors.New("invalid tracked process group record"))
	}
	observed, err := manager.probe.Inspect(ctx, pid)
	if err != nil {
		return false, processUnavailable(pid, err)
	}
	if observed.Alive {
		if !observed.IdentityKnown || observed.Identity == "" {
			return false, processUnavailable(pid, errors.New("process identity is unavailable"))
		}
		if observed.Identity == record.StartedAt {
			return true, nil
		}
	}
	if manager.table == nil {
		return false, processUnavailable(pid, errors.New("process group table is unavailable"))
	}
	entries, err := manager.table.Snapshot(ctx)
	if err != nil {
		return false, processUnavailable(pid, err)
	}
	for _, entry := range entries {
		if entry.GroupID == groupID {
			return false, processUnavailable(pid, errors.New("tracked group remains after its leader exited or changed identity"))
		}
	}
	return false, nil
}

// Terminate revalidates the exact persisted identity immediately before the
// platform-native signal or tree termination.
func (manager NativeProcessManager) Terminate(ctx context.Context, record state.TrackedProcessRecord, force bool) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if record.PID <= 0 || int64(int(record.PID)) != record.PID || record.StartedAt == "" {
		return false, processUnavailable(int(record.PID), errors.New("invalid tracked process record"))
	}
	return terminateNativeRecord(ctx, manager, record, force)
}
