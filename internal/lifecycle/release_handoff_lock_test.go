package lifecycle

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

type handoffLockTestStore struct{ current *state.State }

func (store *handoffLockTestStore) Read(context.Context) (*state.State, error) {
	return store.current, nil
}

func (store *handoffLockTestStore) Update(_ context.Context, mutate func(*state.State) error) error {
	return mutate(store.current)
}

type handoffLockTestLocker struct {
	calls  int
	active bool
	nested bool
}

func (locker *handoffLockTestLocker) With(_ context.Context, _ string, callback func() error) error {
	locker.calls++
	if locker.active {
		locker.nested = true
	}
	locker.active = true
	defer func() { locker.active = false }()
	return callback()
}

type handoffLockTestProcesses struct{}

func (handoffLockTestProcesses) Exists(context.Context, state.TrackedProcessRecord) (bool, error) {
	return false, nil
}

func (handoffLockTestProcesses) Terminate(context.Context, state.TrackedProcessRecord, bool) (bool, error) {
	return true, nil
}

type handoffLockTestGit struct{}

func (handoffLockTestGit) ResetCleanReturn(context.Context, string, bool, []string) error {
	return nil
}

type handoffLockTestPorts struct{}

func (handoffLockTestPorts) Release(context.Context, string) error { return nil }

func TestReleaseAssignmentAlreadyHeldHandoffLockDoesNotReacquire(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "workspace")
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &handoffLockTestStore{current: &state.State{
		Version: state.CurrentVersion,
		Trees:   map[string]state.TreeRecord{},
		Workspaces: map[string]state.WorkspaceRecord{key: {
			Path: path, Lifecycle: state.LifecycleAssigned, UpdatedAt: now.Format(time.RFC3339Nano),
			Processes: []state.TrackedProcessRecord{}, Assignment: &state.AssignmentRecord{ID: "assignment"},
		}},
		Metrics: state.EmptyMetrics(),
	}}
	lifecycleService := New(store, Options{Now: func() time.Time { return now }, NewID: func() string { return "release" }})
	locker := &handoffLockTestLocker{}
	release := NewReleaseService(lifecycleService, ReleaseServiceOptions{
		Reader: store, Processes: handoffLockTestProcesses{}, Git: handoffLockTestGit{}, Ports: handoffLockTestPorts{},
		Locker: locker, LocksRoot: filepath.Join(t.TempDir(), "locks"),
	})

	if err := locker.With(context.Background(), "workspace-lock", func() error {
		_, releaseErr := release.ReleaseAssignment(context.Background(), "assignment", ReleaseOptions{handoffLockHeld: true})
		return releaseErr
	}); err != nil {
		t.Fatalf("ReleaseAssignment returned an error: %v", err)
	}
	if locker.calls != 1 || locker.nested {
		t.Fatalf("handoff lock acquisition = calls %d nested %v, want one outer lock and no nested acquisition", locker.calls, locker.nested)
	}
}
