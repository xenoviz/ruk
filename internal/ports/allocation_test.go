package ports_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/ports"
	"github.com/xenoviz/ruk/internal/state"
)

type allocationStore struct {
	current *state.State
	calls   int
}

func (store *allocationStore) Update(_ context.Context, mutate func(*state.State) error) error {
	store.calls++
	next := cloneAllocationState(store.current)
	if err := mutate(next); err != nil {
		return err
	}
	store.current = next
	return nil
}

type allocationRegistry struct {
	reserved  map[int64]struct{}
	owned     map[int64]string
	commits   int
	withs     int
	commitErr error
}

func (registry *allocationRegistry) WithReservations(_ context.Context, callback func(ports.ReservationSession) error) error {
	registry.withs++
	return callback(&allocationSession{registry: registry})
}

type allocationSession struct{ registry *allocationRegistry }

func (session *allocationSession) Reserved() map[int64]struct{} {
	result := make(map[int64]struct{}, len(session.registry.reserved))
	for value := range session.registry.reserved {
		result[value] = struct{}{}
	}
	return result
}

func (session *allocationSession) Reserve(port int64, assignmentID, _ string) error {
	if _, exists := session.registry.reserved[port]; exists {
		return errors.New("already reserved")
	}
	session.registry.reserved[port] = struct{}{}
	session.registry.owned[port] = assignmentID
	return nil
}

func (session *allocationSession) Commit() error {
	session.registry.commits++
	return session.registry.commitErr
}

type allocationFinder struct {
	values []int64
	seen   []map[int64]struct{}
}

func (finder *allocationFinder) Find(excluded map[int64]struct{}) (int64, error) {
	copyOfExcluded := make(map[int64]struct{}, len(excluded))
	for value := range excluded {
		copyOfExcluded[value] = struct{}{}
	}
	finder.seen = append(finder.seen, copyOfExcluded)
	if len(finder.values) == 0 {
		return 0, errors.New("no port")
	}
	value := finder.values[0]
	finder.values = finder.values[1:]
	return value, nil
}

func TestAllocationServiceReservesPortsBeforePublishingState(t *testing.T) {
	t.Parallel()
	store := allocationState(t)
	registry := &allocationRegistry{reserved: map[int64]struct{}{3001: {}}, owned: map[int64]string{}}
	finder := &allocationFinder{values: []int64{3002, 3003}}
	service := ports.AllocationService{Store: store, Registry: registry, Finder: finder, StatePath: filepath.Join(t.TempDir(), "state.json")}

	workspace, err := service.Allocate(context.Background(), "assignment-a", []string{"api", "debug-ui"})
	if err != nil {
		t.Fatalf("Allocate returned an error: %v", err)
	}
	if !reflect.DeepEqual(workspace.Assignment.Ports, map[string]int64{"api": 3002, "debug-ui": 3003}) {
		t.Fatalf("ports = %#v", workspace.Assignment.Ports)
	}
	if registry.commits != 1 || registry.owned[3002] != "assignment-a" || registry.owned[3003] != "assignment-a" {
		t.Fatalf("registry = %#v commits=%d", registry.owned, registry.commits)
	}
	for index, port := range []int64{3000, 3001} {
		if _, exists := finder.seen[0][port]; !exists {
			t.Fatalf("initial exclusions missing %d at index %d: %#v", port, index, finder.seen[0])
		}
	}
	if _, exists := finder.seen[1][3002]; !exists {
		t.Fatalf("second allocation did not exclude first result: %#v", finder.seen[1])
	}
}

func TestAllocationServiceFailureContracts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		names      []string
		finder     []int64
		commitErr  error
		assignment string
		want       string
		wantLocks  bool
	}{
		{name: "duplicate normalized names", names: []string{"api-url", "api url"}, finder: []int64{3010}, assignment: "assignment-a", want: "unique after normalization"},
		{name: "missing assignment", names: []string{"api"}, finder: []int64{3010}, assignment: "missing", want: "does not exist", wantLocks: true},
		{name: "invalid allocated port", names: []string{"api"}, finder: []int64{0}, assignment: "assignment-a", want: "unavailable port 0", wantLocks: true},
		{name: "registry commit fails", names: []string{"api"}, finder: []int64{3010}, commitErr: errors.New("disk full"), assignment: "assignment-a", want: "disk full", wantLocks: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := allocationState(t)
			registry := &allocationRegistry{reserved: map[int64]struct{}{}, owned: map[int64]string{}, commitErr: test.commitErr}
			finder := &allocationFinder{values: append([]int64(nil), test.finder...)}
			service := ports.AllocationService{Store: store, Registry: registry, Finder: finder, StatePath: filepath.Join(t.TempDir(), "state.json")}
			_, err := service.Allocate(context.Background(), test.assignment, test.names)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if (registry.withs > 0) != test.wantLocks {
				t.Fatalf("registry calls = %d, want lock=%v", registry.withs, test.wantLocks)
			}
			key, _ := state.TreeKey("/workspace/a")
			if got := store.current.Workspaces[key].Assignment.Ports; len(got) != 0 {
				t.Fatalf("state published ports after failure: %#v", got)
			}
		})
	}
}

func allocationState(t *testing.T) *allocationStore {
	t.Helper()
	first := filepath.Clean("/workspace/a")
	second := filepath.Clean("/workspace/b")
	firstKey, err := state.TreeKey(first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := state.TreeKey(second)
	if err != nil {
		t.Fatal(err)
	}
	assignment := func(id string, assignedPorts map[string]int64) *state.AssignmentRecord {
		return &state.AssignmentRecord{ID: id, Owner: "agent", Hostname: "host", AssignedAt: "2026-01-01T00:00:00.000Z", RenewedAt: "2026-01-01T00:00:00.000Z", ExpiresAt: "2026-01-01T08:00:00.000Z", LeaseDurationMinutes: 480, LastActivityAt: "2026-01-01T00:00:00.000Z", LeaseKeepers: []state.LeaseKeeperRecord{}, Ports: assignedPorts}
	}
	return &allocationStore{current: &state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Metrics: state.EmptyMetrics(), Workspaces: map[string]state.WorkspaceRecord{
		firstKey:  {Path: first, Managed: true, Branch: "agent/a", Lifecycle: state.LifecycleAssigned, Assignment: assignment("assignment-a", map[string]int64{}), Processes: []state.TrackedProcessRecord{}, CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z"},
		secondKey: {Path: second, Managed: true, Branch: "agent/b", Lifecycle: state.LifecycleAssigned, Assignment: assignment("assignment-b", map[string]int64{"other": 3000}), Processes: []state.TrackedProcessRecord{}, CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z"},
	}}}
}

func cloneAllocationState(current *state.State) *state.State {
	next := &state.State{Version: current.Version, Trees: current.Trees, Metrics: current.Metrics, Workspaces: make(map[string]state.WorkspaceRecord, len(current.Workspaces))}
	for key, workspace := range current.Workspaces {
		if workspace.Assignment != nil {
			assignment := *workspace.Assignment
			assignment.Ports = make(map[string]int64, len(workspace.Assignment.Ports))
			for name, port := range workspace.Assignment.Ports {
				assignment.Ports[name] = port
			}
			workspace.Assignment = &assignment
		}
		next.Workspaces[key] = workspace
	}
	return next
}
