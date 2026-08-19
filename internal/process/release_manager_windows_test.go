//go:build windows

package process

import (
	"context"
	"errors"
	"testing"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

type windowsDrainProbe struct {
	states []lock.ProcessState
	index  int
}

func (probe *windowsDrainProbe) Inspect(context.Context, int) (lock.ProcessState, error) {
	if len(probe.states) == 0 {
		return lock.ProcessState{}, errors.New("probe exhausted")
	}
	state := probe.states[probe.index]
	if probe.index < len(probe.states)-1 {
		probe.index++
	}
	return state, nil
}

type windowsDrainTable struct {
	snapshots [][]Entry
	index     int
}

func (table *windowsDrainTable) Snapshot(context.Context) ([]Entry, error) {
	if len(table.snapshots) == 0 {
		return nil, errors.New("table exhausted")
	}
	snapshot := table.snapshots[table.index]
	if table.index < len(table.snapshots)-1 {
		table.index++
	}
	return snapshot, nil
}

func TestWindowsFinalDrainObservesLeaderlessDescendants(t *testing.T) {
	probe := &windowsDrainProbe{states: []lock.ProcessState{
		{Alive: false},
		{Alive: false},
	}}
	table := &windowsDrainTable{snapshots: [][]Entry{
		{{PID: 43, ParentPID: 42}},
		{},
	}}
	err := waitForWindowsTreeDrain(context.Background(), probe, table, state.TrackedProcessRecord{PID: 42, StartedAt: "windows:leader"})
	if err != nil {
		t.Fatalf("waitForWindowsTreeDrain returned %v", err)
	}
	if table.index != 1 {
		t.Fatalf("snapshots consumed = %d, want final descendant drain observation", table.index+1)
	}
}

func TestWindowsFinalDrainRejectsLeaderPIDReuse(t *testing.T) {
	probe := &windowsDrainProbe{states: []lock.ProcessState{
		{Alive: false},
		{Alive: true, IdentityKnown: true, Identity: "windows:replacement"},
	}}
	table := &windowsDrainTable{snapshots: [][]Entry{{{PID: 43, ParentPID: 42}}}}
	err := waitForWindowsTreeDrain(context.Background(), probe, table, state.TrackedProcessRecord{PID: 42, StartedAt: "windows:leader"})
	var unavailable *IdentityUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %T %v, want IdentityUnavailableError", err, err)
	}
}

func TestDescendantPIDsAfterLeaderExitIncludesTransitiveChildren(t *testing.T) {
	got := descendantPIDsAfterLeaderExit([]Entry{
		{PID: 43, ParentPID: 42},
		{PID: 44, ParentPID: 43},
	}, 42)
	if len(got) != 2 || got[0] != 43 || got[1] != 44 {
		t.Fatalf("descendants = %v, want [43 44]", got)
	}
}
