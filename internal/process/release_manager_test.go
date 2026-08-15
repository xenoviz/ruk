package process_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type releaseProbe struct {
	states []lock.ProcessState
	index  int
}

func (probe *releaseProbe) Inspect(context.Context, int) (lock.ProcessState, error) {
	if len(probe.states) == 0 {
		return lock.ProcessState{}, errors.New("probe exhausted")
	}
	state := probe.states[0]
	if len(probe.states) > 1 {
		probe.states = probe.states[1:]
	}
	probe.index++
	return state, nil
}

type releaseTable []processpkg.Entry

func (table releaseTable) Snapshot(context.Context) ([]processpkg.Entry, error) {
	return append([]processpkg.Entry(nil), table...), nil
}

type releaseGroupSignaler struct {
	group int
	kind  processpkg.SignalKind
	calls int
}

func (signaler *releaseGroupSignaler) SignalGroup(_ context.Context, group int, kind processpkg.SignalKind) error {
	signaler.group = group
	signaler.kind = kind
	signaler.calls++
	return nil
}

type releasePIDSignaler struct {
	pid   int
	kind  processpkg.SignalKind
	calls int
}

func (signaler *releasePIDSignaler) SignalPID(_ context.Context, pid int, kind processpkg.SignalKind) error {
	signaler.pid = pid
	signaler.kind = kind
	signaler.calls++
	return nil
}

type releaseTreeTerminator struct {
	record state.TrackedProcessRecord
	force  bool
	calls  int
}

func (terminator *releaseTreeTerminator) TerminateTree(_ context.Context, record state.TrackedProcessRecord, force bool) (bool, error) {
	terminator.record = record
	terminator.force = force
	terminator.calls++
	return true, nil
}

func TestNativeProcessManagerExistsDelegatesFailClosedTracker(t *testing.T) {
	manager := processpkg.NewNativeProcessManager(processpkg.ReleaseManagerOptions{
		Probe: &releaseProbe{states: []lock.ProcessState{{Alive: true, IdentityKnown: true, Identity: "started"}}},
		Table: releaseTable{{PID: 42, ParentPID: 1, GroupID: 42}},
	})
	exists, err := manager.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "started"})
	if err != nil || !exists {
		t.Fatalf("Exists = %v, %v; want live exact process", exists, err)
	}

	manager = processpkg.NewNativeProcessManager(processpkg.ReleaseManagerOptions{
		Probe: &releaseProbe{states: []lock.ProcessState{{Alive: true, IdentityKnown: false}}},
		Table: releaseTable{{PID: 42, ParentPID: 1, GroupID: 42}},
	})
	exists, err = manager.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "started"})
	if err == nil || exists || !strings.Contains(err.Error(), "could not be identified") {
		t.Fatalf("unknown identity Exists = %v, %v", exists, err)
	}
}

func TestNativeProcessManagerAttachedRevalidatesPIDBeforeGracefulAndForceSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses injected/native tree termination")
	}
	for _, test := range []struct {
		name  string
		force bool
		kind  processpkg.SignalKind
	}{
		{name: "graceful", kind: processpkg.SignalGraceful},
		{name: "force", force: true, kind: processpkg.SignalForce},
	} {
		t.Run(test.name, func(t *testing.T) {
			signaler := &releasePIDSignaler{}
			probe := &releaseProbe{states: []lock.ProcessState{{Alive: true, IdentityKnown: true, Identity: "started"}}}
			manager := processpkg.NewNativeProcessManager(processpkg.ReleaseManagerOptions{Probe: probe, PIDSignaler: signaler})
			terminated, err := manager.Terminate(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "started"}, test.force)
			if err != nil || !terminated {
				t.Fatalf("Terminate = %v, %v", terminated, err)
			}
			if signaler.calls != 1 || signaler.pid != 42 || signaler.kind != test.kind {
				t.Fatalf("PID signal = %#v, want pid 42 kind %d", signaler, test.kind)
			}
			if probe.index != 1 {
				t.Fatalf("identity probe calls = %d, want one immediate fence", probe.index)
			}
		})
	}
}

func TestNativeProcessManagerRetainsPIDReuseAndUnknownIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses injected/native tree termination")
	}
	for _, test := range []struct {
		name  string
		state lock.ProcessState
	}{
		{name: "PID reused", state: lock.ProcessState{Alive: true, IdentityKnown: true, Identity: "replacement"}},
		{name: "identity unknown", state: lock.ProcessState{Alive: true, IdentityKnown: false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			signaler := &releasePIDSignaler{}
			manager := processpkg.NewNativeProcessManager(processpkg.ReleaseManagerOptions{Probe: &releaseProbe{states: []lock.ProcessState{test.state}}, PIDSignaler: signaler})
			terminated, err := manager.Terminate(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "started"}, false)
			if err == nil || terminated || signaler.calls != 0 {
				t.Fatalf("Terminate = %v, %v; signaler = %#v", terminated, err, signaler)
			}
		})
	}
}

func TestNativeProcessManagerDetachedUsesGroupMembershipFence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses injected/native tree termination")
	}
	group := int64(42)
	signaler := &releaseGroupSignaler{}
	manager := processpkg.NewNativeProcessManager(processpkg.ReleaseManagerOptions{
		Probe:         &releaseProbe{states: []lock.ProcessState{{Alive: true, IdentityKnown: true, Identity: "started"}, {Alive: true, IdentityKnown: true, Identity: "started"}}},
		Table:         releaseTable{{PID: 42, ParentPID: 1, GroupID: 42}, {PID: 43, ParentPID: 42, GroupID: 42}},
		GroupSignaler: signaler,
	})
	terminated, err := manager.Terminate(context.Background(), state.TrackedProcessRecord{PID: 42, GroupID: &group, StartedAt: "started"}, true)
	if err != nil || !terminated || signaler.calls != 1 || signaler.group != 42 || signaler.kind != processpkg.SignalForce {
		t.Fatalf("Terminate = %v, %v; group signaler = %#v", terminated, err, signaler)
	}
}

func TestNativeProcessManagerDetachedLeaderlessGroupFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses injected/native tree termination")
	}
	group := int64(42)
	signaler := &releaseGroupSignaler{}
	manager := processpkg.NewNativeProcessManager(processpkg.ReleaseManagerOptions{
		Probe:         &releaseProbe{states: []lock.ProcessState{{Alive: false}}},
		Table:         releaseTable{{PID: 43, ParentPID: 1, GroupID: 42}},
		GroupSignaler: signaler,
	})
	terminated, err := manager.Terminate(context.Background(), state.TrackedProcessRecord{PID: 42, GroupID: &group, StartedAt: "started"}, false)
	if err == nil || terminated || signaler.calls != 0 || !strings.Contains(err.Error(), "could not be identified") {
		t.Fatalf("leaderless Terminate = %v, %v; group signaler = %#v", terminated, err, signaler)
	}
}

func TestNativeProcessManagerWindowsTreeSeamIsInjectable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only tree seam")
	}
	terminator := &releaseTreeTerminator{}
	manager := processpkg.NewNativeProcessManager(processpkg.ReleaseManagerOptions{TreeTerminator: terminator})
	record := state.TrackedProcessRecord{PID: 42, StartedAt: "started"}
	terminated, err := manager.Terminate(context.Background(), record, true)
	if err != nil || !terminated || terminator.calls != 1 || !terminator.force || terminator.record.PID != record.PID {
		t.Fatalf("tree Terminate = %v, %v; terminator = %#v", terminated, err, terminator)
	}
}
