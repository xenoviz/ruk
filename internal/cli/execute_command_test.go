package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/lifecycle"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type executeStore struct {
	current        *state.State
	updateContexts []context.Context
}

func (store *executeStore) Read(context.Context) (*state.State, error) { return store.current, nil }
func (store *executeStore) Update(ctx context.Context, mutate func(*state.State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.updateContexts = append(store.updateContexts, ctx)
	return mutate(store.current)
}

type executeRunningChild struct {
	pid         int
	waitStarted chan struct{}
	done        chan processpkg.ExitStatus
	startOnce   sync.Once
	finishOnce  sync.Once
}

func (child *executeRunningChild) PID() int { return child.pid }
func (child *executeRunningChild) Wait() processpkg.ExitStatus {
	child.startOnce.Do(func() { close(child.waitStarted) })
	return <-child.done
}
func (child *executeRunningChild) Signal(os.Signal) error { return nil }

type executeCleanup struct {
	child     *executeRunningChild
	cleanup   int
	verify    int
	verifyCtx context.Context
}

func (cleaner *executeCleanup) Cleanup(_ context.Context, _ processpkg.Child, _ state.TrackedProcessRecord) error {
	cleaner.cleanup++
	cleaner.child.finishOnce.Do(func() { close(cleaner.child.done) })
	return nil
}

func (cleaner *executeCleanup) Exists(ctx context.Context, _ state.TrackedProcessRecord) (bool, error) {
	cleaner.verify++
	cleaner.verifyCtx = ctx
	return false, nil
}

type executeRecoveryProcesses struct {
	ctx   context.Context
	calls int
}

func (processes *executeRecoveryProcesses) Exists(ctx context.Context, _ state.TrackedProcessRecord) (bool, error) {
	processes.ctx = ctx
	processes.calls++
	return false, nil
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
	child               processpkg.Child
	called              bool
	spawnContextDoneNil bool
}

func (spawner *executeSpawner) Spawn(ctx context.Context, _ processpkg.SpawnRequest) (processpkg.Child, error) {
	spawner.called = true
	spawner.spawnContextDoneNil = ctx.Done() == nil
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

type executeProcesses struct {
	alive bool
	err   error
}

func (processes executeProcesses) Exists(context.Context, state.TrackedProcessRecord) (bool, error) {
	return processes.alive, processes.err
}

func (executeProcesses) Terminate(context.Context, state.TrackedProcessRecord, bool) (bool, error) {
	return false, nil
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

func TestExecuteSupervisesCancellationAcrossNativeChildBoundary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, runner := executeFixture(t, path)
	spawner := runner.Spawner.(*executeSpawner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(context.Context, string, string) error { return nil },
	})

	if _, err := service.Execute(ctx, cli.ExecuteInput{
		AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached,
	}); err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	if !spawner.spawnContextDoneNil {
		t.Fatal("managed child was spawned with a cancelable context; cancellation cannot be supervised safely")
	}
}

func TestExecuteCancellationCleansUpAndRemovesRecordWithRecoveryContext(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, _ := executeFixture(t, path)
	child := &executeRunningChild{pid: 42, waitStarted: make(chan struct{}), done: make(chan processpkg.ExitStatus, 1)}
	cleanup := &executeCleanup{child: child}
	spawner := &executeSpawner{child: child}
	runner := processpkg.Runner{
		Spawner:   spawner,
		Describer: executeDescriber{record: state.TrackedProcessRecord{PID: 42, StartedAt: "identity", GroupID: int64Pointer(42)}},
		Cleaner:   cleanup,
	}
	processes := &executeRecoveryProcesses{}
	var releaseCtx context.Context
	var releaseID string
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner, Processes: processes,
		Release: func(ctx context.Context, assignmentID string) error {
			releaseID = assignmentID
			releaseCtx = ctx
			return nil
		},
		Synchronize: func(context.Context, string, string) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan struct {
		result cli.ExecuteResult
		err    error
	}, 1)
	go func() {
		result, err := service.Execute(ctx, cli.ExecuteInput{
			AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Detached, Exec: true,
		})
		resultCh <- struct {
			result cli.ExecuteResult
			err    error
		}{result: result, err: err}
	}()
	<-child.waitStarted
	cancel()
	completed := <-resultCh
	if completed.err == nil || !errors.Is(completed.err, context.Canceled) {
		t.Fatalf("canceled Execute error=%v, want context cancellation", completed.err)
	}
	if !completed.result.Started {
		t.Fatal("canceled execution did not report a started managed child")
	}
	if !completed.result.Released {
		t.Fatal("canceled exec did not release after safe record removal")
	}
	if cleanup.cleanup != 1 || cleanup.verify != 1 || processes.calls != 1 {
		t.Fatalf("cleanup calls=%d verifier calls=%d final process calls=%d, want 1/1/1", cleanup.cleanup, cleanup.verify, processes.calls)
	}
	if processes.ctx == nil || processes.ctx.Err() != nil {
		t.Fatalf("final process context=%v, want active recovery context", processes.ctx)
	}
	if _, ok := processes.ctx.Deadline(); !ok {
		t.Fatal("final process context has no bounded deadline")
	}
	if cleanup.verifyCtx == nil || cleanup.verifyCtx == processes.ctx {
		t.Fatal("runner verifier unexpectedly reused execute finalization context")
	}
	if len(store.updateContexts) < 2 || store.updateContexts[len(store.updateContexts)-1] != processes.ctx {
		t.Fatal("record removal did not use the same recovery context as final process verification")
	}
	if releaseCtx != processes.ctx {
		t.Fatal("exec release did not use the same recovery context as final process verification")
	}
	if releaseID != "assignment-1" {
		t.Fatalf("release assignment=%q, want assignment-1", releaseID)
	}
	key, _ := state.TreeKey(path)
	if len(store.current.Workspaces[key].Processes) != 0 {
		t.Fatalf("canceled child record was not removed: %#v", store.current.Workspaces[key].Processes)
	}
}

func TestExecuteAttachedCancellationRemovesRecordWithRecoveryContext(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, _ := executeFixture(t, path)
	child := &executeRunningChild{pid: 42, waitStarted: make(chan struct{}), done: make(chan processpkg.ExitStatus, 1)}
	cleanup := &executeCleanup{child: child}
	runner := processpkg.Runner{
		Spawner:   &executeSpawner{child: child},
		Describer: executeDescriber{record: state.TrackedProcessRecord{PID: 42, StartedAt: "identity"}},
		Cleaner:   cleanup,
	}
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(context.Context, string, string) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := service.Execute(ctx, cli.ExecuteInput{
			AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached,
		})
		resultCh <- err
	}()
	<-child.waitStarted
	cancel()
	err := <-resultCh
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attached Execute error=%v, want context cancellation", err)
	}
	if cleanup.cleanup != 1 {
		t.Fatalf("attached cleanup calls=%d, want 1", cleanup.cleanup)
	}
	if len(store.updateContexts) < 2 {
		t.Fatal("attached cancellation did not reach durable process removal")
	}
	removalCtx := store.updateContexts[len(store.updateContexts)-1]
	if removalCtx.Err() != nil {
		t.Fatalf("attached removal context=%v, want active recovery context", removalCtx)
	}
	if _, ok := removalCtx.Deadline(); !ok {
		t.Fatal("attached removal context has no bounded deadline")
	}
	key, _ := state.TreeKey(path)
	if len(store.current.Workspaces[key].Processes) != 0 {
		t.Fatalf("attached canceled child record was not removed: %#v", store.current.Workspaces[key].Processes)
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
			workspace := store.current.Workspaces[key]
			assignment := *workspace.Assignment
			assignment.ID = "new-owner"
			workspace.Assignment = &assignment
			store.current.Workspaces[key] = workspace
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
	service := cli.NewExecuteService(cli.ExecuteOptions{Lifecycle: lifecycleService, Reader: store, Runner: runner, Processes: executeProcesses{}, Synchronize: func(context.Context, string, string) error { return nil }})
	result, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Detached, Signals: signals})
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	if result.ExitCode != 143 || len(forwarder.signals) != 1 || forwarder.signals[0] != syscall.SIGINT {
		t.Fatalf("signal execution = %#v / %#v", result, forwarder.signals)
	}
}

func TestExecuteDetachedRetainsRecordWhenDescendantsRemain(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, _ := executeFixture(t, path)
	child := &executeChild{pid: 42, status: processpkg.ExitStatus{Code: 0}}
	runner := processpkg.Runner{
		Spawner:   &executeSpawner{child: child},
		Describer: executeDescriber{record: state.TrackedProcessRecord{PID: 42, StartedAt: "identity", GroupID: int64Pointer(42)}},
	}
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner, Processes: executeProcesses{alive: true},
		Synchronize: func(context.Context, string, string) error { return nil },
	})
	_, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Detached})
	if err == nil || !strings.Contains(err.Error(), "remains after its leader exited") {
		t.Fatalf("Execute error = %v, want surviving detached tree", err)
	}
	key, _ := state.TreeKey(path)
	if len(store.current.Workspaces[key].Processes) != 1 {
		t.Fatalf("surviving detached process record was removed: %#v", store.current.Workspaces[key].Processes)
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

func TestExecuteExecReleasesAssignmentWhenSynchronizationFailsBeforeSpawn(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, runner := executeFixture(t, path)
	spawner := runner.Spawner.(*executeSpawner)
	syncErr := errors.New("dependency synchronization failed")
	released := false
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(context.Context, string, string) error { return syncErr },
		Release:     func(context.Context, string) error { released = true; return nil },
	})
	result, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached, Exec: true})
	if !errors.Is(err, syncErr) || !released || !result.Released || spawner.called {
		t.Fatalf("pre-spawn exec cleanup = result %#v error %v released %v spawned %v", result, err, released, spawner.called)
	}
}

func TestExecuteExecSurfacesRecoveryWhenPreSpawnReleaseFails(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "workspace")
	store, lifecycleService, runner := executeFixture(t, path)
	releaseErr := errors.New("release lock unavailable")
	service := cli.NewExecuteService(cli.ExecuteOptions{
		Lifecycle: lifecycleService, Reader: store, Runner: runner,
		Synchronize: func(context.Context, string, string) error { return errors.New("sync failed") },
		Release:     func(context.Context, string) error { return releaseErr },
	})
	_, err := service.Execute(context.Background(), cli.ExecuteInput{AssignmentID: "assignment-1", WorkspacePath: path, Command: []string{"tool"}, Mode: processpkg.Attached, Exec: true})
	if err == nil || !strings.Contains(err.Error(), "retained for recovery") || !errors.Is(err, releaseErr) {
		t.Fatalf("pre-spawn release recovery error = %v", err)
	}
}

func int64Pointer(value int64) *int64 { return &value }
