package process_test

import (
	"context"
	"errors"
	"testing"

	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type orderedProbe struct {
	states []lock.ProcessState
	calls  int
}

func (probe *orderedProbe) Inspect(context.Context, int) (lock.ProcessState, error) {
	if probe.calls >= len(probe.states) {
		return lock.ProcessState{}, errors.New("unexpected identity probe")
	}
	state := probe.states[probe.calls]
	probe.calls++
	return state, nil
}

type recordingSignaler struct {
	group int
	kind  processpkg.SignalKind
	calls int
}

func (signaler *recordingSignaler) SignalGroup(_ context.Context, group int, kind processpkg.SignalKind) error {
	signaler.group = group
	signaler.kind = kind
	signaler.calls++
	return nil
}

func TestGroupTerminatorRevalidatesIdentityBeforeSignal(t *testing.T) {
	t.Parallel()

	probe := &orderedProbe{states: []lock.ProcessState{
		{Alive: true, IdentityKnown: true, Identity: "original"},
		{Alive: true, IdentityKnown: true, Identity: "original"},
	}}
	signaler := &recordingSignaler{}
	terminator := processpkg.GroupTerminator{
		Probe:    probe,
		Table:    staticProcessTable{{PID: 42, ParentPID: 1, GroupID: 42}},
		Signaler: signaler,
	}
	record := state.TrackedProcessRecord{PID: 42, GroupID: processInt64Pointer(42), StartedAt: "original"}

	terminated, err := terminator.TerminateGroup(context.Background(), record, false)
	if err != nil {
		t.Fatalf("TerminateGroup returned an error: %v", err)
	}
	if !terminated {
		t.Fatal("matching process group was not terminated")
	}
	if probe.calls != 2 {
		t.Fatalf("identity probe calls = %d, want 2", probe.calls)
	}
	if signaler.calls != 1 || signaler.group != 42 || signaler.kind != processpkg.SignalGraceful {
		t.Fatalf("signal call = %#v", signaler)
	}
}

func TestGroupTerminatorRefusesIdentityChangedBeforeSignal(t *testing.T) {
	t.Parallel()

	probe := &orderedProbe{states: []lock.ProcessState{
		{Alive: true, IdentityKnown: true, Identity: "original"},
		{Alive: true, IdentityKnown: true, Identity: "replacement"},
	}}
	signaler := &recordingSignaler{}
	terminator := processpkg.GroupTerminator{
		Probe:    probe,
		Table:    staticProcessTable{{PID: 42, ParentPID: 1, GroupID: 42}},
		Signaler: signaler,
	}
	record := state.TrackedProcessRecord{PID: 42, GroupID: processInt64Pointer(42), StartedAt: "original"}

	_, err := terminator.TerminateGroup(context.Background(), record, true)
	var unavailable *processpkg.IdentityUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("TerminateGroup error = %v, want IdentityUnavailableError", err)
	}
	if signaler.calls != 0 {
		t.Fatalf("unsafe signal calls = %d", signaler.calls)
	}
}

func TestGroupTerminatorRetainsLeaderlessGroup(t *testing.T) {
	t.Parallel()

	probe := &orderedProbe{states: []lock.ProcessState{{}}}
	signaler := &recordingSignaler{}
	terminator := processpkg.GroupTerminator{
		Probe:    probe,
		Table:    staticProcessTable{{PID: 43, ParentPID: 1, GroupID: 42}},
		Signaler: signaler,
	}
	record := state.TrackedProcessRecord{PID: 42, GroupID: processInt64Pointer(42), StartedAt: "original"}

	_, err := terminator.TerminateGroup(context.Background(), record, false)
	var unavailable *processpkg.IdentityUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("TerminateGroup error = %v, want IdentityUnavailableError", err)
	}
	if signaler.calls != 0 {
		t.Fatalf("unsafe signal calls = %d", signaler.calls)
	}
}

func processInt64Pointer(value int64) *int64 {
	return &value
}
