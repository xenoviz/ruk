package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/lifecycle"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type executeStore struct {
	current *state.State
}

func (store *executeStore) Read(context.Context) (*state.State, error) { return store.current, nil }
func (store *executeStore) Update(ctx context.Context, mutate func(*state.State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return mutate(store.current)
}

type executeChild struct {
	pid    int
	status processpkg.ExitStatus
	waited bool
}

func (child *executeChild) PID() int { return child.pid }
func (child *executeChild) Wait() processpkg.ExitStatus {
	child.waited = true
	return child.status
}
func (child *executeChild) Signal(os.Signal) error { return nil }

type executeSpawner struct {
	child  *executeChild
	called bool
}

func (spawner *executeSpawner) Spawn(context.Context, processpkg.SpawnRequest) (processpkg.Child, error) {
	spawner.called = true
	return spawner.child, nil
}

type executeDescriber struct {
	record state.TrackedProcessRecord
}

func (describer executeDescriber) Describe(_ context.Context, _ int, _ processpkg.ProcessMode, _ []string) (state.TrackedProcessRecord, error) {
	return describer.record, nil
}

type executeForwarder struct {
	signals []os.Signal
}

func (forwarder *executeForwarder) Forward(_ context.Context, _ state.TrackedProcessRecord, signal os.Signal) error {
	forwarder.signals = append(forwarder.signals, signal)
	return nil
}

func executeFixture(t *testing.T, path string) (*executeStore, *lifecycle.Service, processpkg.Runner) {
	t.Helper()
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	store := &executeStore{current: &state.State{
		Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{}, Metrics: state.EmptyMetrics(),
	}}
	store.current.Workspaces[key] = state.WorkspaceRecord{
		Path: path, Managed: true, Lifecycle: state.LifecycleAssigned,
		Assignment: &state.AssignmentRecord{ID: "assignment-1", Owner: "owner", Hostname: "host", AssignedAt: "2026-01-01T00:00:00.000Z", RenewedAt: "2026-01-01T00:00:00.000Z", ExpiresAt: "2026-01-01T08:00:00.000Z", LeaseDurationMinutes: 480, LastActivityAt: "2026-01-01T00:00:00.000Z", LeaseKeepers: []state.LeaseKeeperRecord{}, Ports: map[string]int64{}},
		Processes:  []state.TrackedProcessRecord{}, CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	lifecycleService := lifecycle.New(store, lifecycle.Options{Now: func() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) }, NewID: func() string { return "unused" }})
	child := &executeChild{pid: 42, status: processpkg.ExitStatus{Code: 0}}
	runner := processpkg.Runner{Spawner: &executeSpawner{child: child}, Describer: executeDescriber{record: state.TrackedProcessRecord{PID: 42, StartedAt: "identity", GroupID: int64Pointer(42)}}}
	return store, lifecycleService, runner
}

func TestExecuteSynchronizesTracksRemovesAndMaintainsActivity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, runner := executeFixture(t, path)
	synchronized := false
	activityStarted, activityFinished := false, false
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(_ context.Context, assignmentID, workspace string) error {
			synchronized = assignmentID == "assignment-1" && workspace == path
			return nil
		},
		Activity: func(ctx context.Context, assignmentID string, operation func(context.Context) error) error {
			activityStarted = assignmentID == "assignment-1"
			err := operation(ctx)
			activityFinished = true
			return err
		},
	})
	result, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached})
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	if !synchronized || !activityStarted || !activityFinished || result.ExitCode != 0 {
		t.Fatalf("execution result/activity = %#v / %v %v %v", result, synchronized, activityStarted, activityFinished)
	}
	key, _ := state.TreeKey(path)
	if len(store.current.Workspaces[key].Processes) != 0 {
		t.Fatalf("tracked process was not removed: %#v", store.current.Workspaces[key].Processes)
	}
}

func TestExecuteRejectsAssignmentFenceChangeAfterSynchronization(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, runner := executeFixture(t, path)
	spawner := runner.Spawner.(*executeSpawner)
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(context.Context, string, string) error {
			key, _ := state.TreeKey(path)
			assignment := *store.current.Workspaces[key].Assignment
			assignment.ID = "new-owner"
			store.current.Workspaces[key].Assignment = &assignment
			return nil
		},
	})
	_, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached})
	if err == nil {
		t.Fatal("Execute succeeded after assignment ownership changed")
	}
	if spawner.called {
		t.Fatal("process spawned after assignment fence changed")
	}
}

func TestExecuteForwardsPendingDetachedSignalsAndPreservesSignalExitStatus(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, _ := executeFixture(t, path)
	child := &executeChild{pid: 42, status: processpkg.ExitStatus{Code: -1, Signal: syscall.SIGTERM}}
	spawner := &executeSpawner{child: child}
	forwarder := &executeForwarder{}
	runner := processpkg.Runner{Spawner: spawner, Describer: executeDescriber{record: state.TrackedProcessRecord{PID: 42, StartedAt: "identity", GroupID: int64Pointer(42)}}, Forwarder: forwarder}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGINT
	service := cli.NewExecuteService(cli.ExecuteOptions{Lifecycle: lifecycleService, Reader: store, Runner: runner, Synchronize: func(context.Context, string, string) error { return nil }})
	result, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Detached, Signals: signals})
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	if result.ExitCode != 143 || len(forwarder.signals) != 1 || forwarder.signals[0] != syscall.SIGINT {
		t.Fatalf("signal execution = %#v / %#v", result, forwarder.signals)
	}
}

func TestExecuteExecReleasesOnlyAfterTrackedProcessRemoval(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, runner := executeFixture(t, path)
	releasedAfterRemoval := false
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(context.Context, string, string) error { return nil },
		Release: func(_ context.Context, assignmentID string) error {
			key, _ := state.TreeKey(path)
			releasedAfterRemoval = assignmentID == "assignment-1" && len(store.current.Workspaces[key].Processes) == 0
			return nil
		},
	})
	result, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached, Exec: true})
	if err != nil || !result.Released || !releasedAfterRemoval {
		t.Fatalf("exec result/release = %#v / %v", result, releasedAfterRemoval)
	}
}

func TestExecuteActivityFailureStopsBeforeSpawn(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, runner := executeFixture(t, path)
	spawner := runner.Spawner.(*executeSpawner)
	activityErr := errors.New("activity renewal failed")
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(context.Context, string, string) error { return nil },
		Activity:    func(context.Context, string, func(context.Context) error) error { return activityErr },
	})
	_, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached})
	if !errors.Is(err, activityErr) || spawner.called {
		t.Fatalf("activity failure/spawn = %v / %v", err, spawner.called)
	}
}

func int64Pointer(value int64) *int64 { return &value }
