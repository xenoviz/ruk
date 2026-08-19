package process_test

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type signalProbe struct {
	states []lock.ProcessState
	calls  int
}

func (probe *signalProbe) Inspect(context.Context, int) (lock.ProcessState, error) {
	probe.calls++
	if len(probe.states) == 0 {
		return lock.ProcessState{}, errors.New("signal probe exhausted")
	}
	state := probe.states[0]
	if len(probe.states) > 1 {
		probe.states = probe.states[1:]
	}
	return state, nil
}

type signalTable []processpkg.Entry

func (table signalTable) Snapshot(context.Context) ([]processpkg.Entry, error) {
	return append([]processpkg.Entry(nil), table...), nil
}

type signalGroup struct {
	group  int
	signal os.Signal
	calls  int
}

func (signaler *signalGroup) SignalGroupSignal(_ context.Context, group int, signal os.Signal) error {
	signaler.group = group
	signaler.signal = signal
	signaler.calls++
	return nil
}

type signalTree struct {
	record state.TrackedProcessRecord
	signal os.Signal
	calls  int
}

func (signaler *signalTree) ForwardTree(_ context.Context, record state.TrackedProcessRecord, signal os.Signal) error {
	signaler.record = record
	signaler.signal = signal
	signaler.calls++
	return nil
}

func TestNativeSignalForwarderForwardsExactPOSIXSignalsAfterIdentityFence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows forwards through its native tree seam")
	}
	group := int64(42)
	for _, test := range []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "interrupt", signal: syscall.SIGINT},
		{name: "terminate", signal: syscall.SIGTERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := &signalProbe{states: []lock.ProcessState{
				{Alive: true, IdentityKnown: true, Identity: "started"},
				{Alive: true, IdentityKnown: true, Identity: "started"},
			}}
			groupSignal := &signalGroup{}
			forwarder := processpkg.NewNativeSignalForwarder(processpkg.SignalForwarderOptions{
				Probe: probe, Table: signalTable{{PID: 42, ParentPID: 1, GroupID: 42}}, GroupSignal: groupSignal,
			})
			if err := forwarder.Forward(context.Background(), state.TrackedProcessRecord{PID: 42, GroupID: &group, StartedAt: "started"}, test.signal); err != nil {
				t.Fatalf("Forward returned an error: %v", err)
			}
			if groupSignal.calls != 1 || groupSignal.group != 42 || groupSignal.signal != test.signal {
				t.Fatalf("group signal = %#v, want group 42 signal %v", groupSignal, test.signal)
			}
			if probe.calls != 2 {
				t.Fatalf("identity probe calls = %d, want initial plus immediate revalidation", probe.calls)
			}
		})
	}
}

func TestNativeSignalForwarderFailsClosedOnPIDReuseOrUnknownIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows forwards through its native tree seam")
	}
	group := int64(42)
	for _, test := range []struct {
		name   string
		states []lock.ProcessState
	}{
		{name: "leader reused before signal", states: []lock.ProcessState{{Alive: true, IdentityKnown: true, Identity: "started"}, {Alive: true, IdentityKnown: true, Identity: "replacement"}}},
		{name: "leader identity unknown", states: []lock.ProcessState{{Alive: true, IdentityKnown: false}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			groupSignal := &signalGroup{}
			forwarder := processpkg.NewNativeSignalForwarder(processpkg.SignalForwarderOptions{
				Probe: &signalProbe{states: test.states}, Table: signalTable{{PID: 42, ParentPID: 1, GroupID: 42}}, GroupSignal: groupSignal,
			})
			err := forwarder.Forward(context.Background(), state.TrackedProcessRecord{PID: 42, GroupID: &group, StartedAt: "started"}, syscall.SIGINT)
			if err == nil || groupSignal.calls != 0 || !strings.Contains(err.Error(), "could not be identified") {
				t.Fatalf("Forward error = %v; group signal = %#v", err, groupSignal)
			}
		})
	}
}

func TestNativeSignalForwarderRequiresRecordedGroupMembership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows forwards through its native tree seam")
	}
	group := int64(42)
	groupSignal := &signalGroup{}
	forwarder := processpkg.NewNativeSignalForwarder(processpkg.SignalForwarderOptions{
		Probe: &signalProbe{states: []lock.ProcessState{{Alive: true, IdentityKnown: true, Identity: "started"}}},
		Table: signalTable{{PID: 43, ParentPID: 1, GroupID: 42}}, GroupSignal: groupSignal,
	})
	err := forwarder.Forward(context.Background(), state.TrackedProcessRecord{PID: 42, GroupID: &group, StartedAt: "started"}, syscall.SIGTERM)
	if err == nil || groupSignal.calls != 0 || !strings.Contains(err.Error(), "could not be identified") {
		t.Fatalf("leaderless Forward error = %v; group signal = %#v", err, groupSignal)
	}
}

func TestNativeSignalForwarderRejectsUnsupportedSignalsBeforeMutation(t *testing.T) {
	group := int64(42)
	groupSignal := &signalGroup{}
	forwarder := processpkg.NewNativeSignalForwarder(processpkg.SignalForwarderOptions{GroupSignal: groupSignal})
	err := forwarder.Forward(context.Background(), state.TrackedProcessRecord{PID: 42, GroupID: &group, StartedAt: "started"}, os.Kill)
	if err == nil || !strings.Contains(err.Error(), "only SIGINT and SIGTERM") || groupSignal.calls != 0 {
		t.Fatalf("unsupported Forward error = %v; group signal = %#v", err, groupSignal)
	}
}

func TestNativeSignalForwarderUsesInjectedWindowsTreeSeam(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only tree seam")
	}
	tree := &signalTree{}
	forwarder := processpkg.NewNativeSignalForwarder(processpkg.SignalForwarderOptions{TreeSignal: tree})
	record := state.TrackedProcessRecord{PID: 42, StartedAt: "started"}
	if err := forwarder.Forward(context.Background(), record, syscall.SIGINT); err != nil {
		t.Fatalf("Forward returned an error: %v", err)
	}
	if tree.calls != 1 || tree.record.PID != record.PID || tree.signal != syscall.SIGINT {
		t.Fatalf("tree signal = %#v", tree)
	}
}
