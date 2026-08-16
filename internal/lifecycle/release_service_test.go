package lifecycle_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

type releaseStore struct {
	current    *state.State
	failFinish error
}

func (store *releaseStore) Update(_ context.Context, mutate func(*state.State) error) error {
	if store.failFinish != nil {
		for _, workspace := range store.current.Workspaces {
			if workspace.Lifecycle == state.LifecycleReturning && len(workspace.Processes) == 0 {
				err := store.failFinish
				store.failFinish = nil
				return err
			}
		}
	}
	return mutate(store.current)
}

func (store *releaseStore) Read(context.Context) (*state.State, error) { return store.current, nil }

type releaseProcesses struct {
	exists      []bool
	keepAlive   bool
	terminate   []bool
	existsCalls int
	calls       int
	err         error
}

func (processes *releaseProcesses) Exists(context.Context, state.TrackedProcessRecord) (bool, error) {
	processes.existsCalls++
	if processes.err != nil {
		return false, processes.err
	}
	if len(processes.exists) == 0 {
		return processes.keepAlive, nil
	}
	result := processes.exists[0]
	processes.exists = processes.exists[1:]
	return result, nil
}

func TestReleaseServicePollsUntilTrackedTreeDrains(t *testing.T) {
	store, service, assignmentID, _ := newReleaseFixture(t, nil)
	addTrackedProcess(t, store, assignmentID)
	processes := &releaseProcesses{exists: []bool{true, true, false}}
	release := lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: processes, Git: &releaseGit{}, Ports: &releasePorts{}, Locker: &releaseLocker{}, LocksRoot: t.TempDir(),
		ProcessDrainTimeout: time.Second, ProcessPollInterval: time.Millisecond,
	})

	if _, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{}); err != nil {
		t.Fatalf("Release returned an error: %v", err)
	}
	if processes.existsCalls != 3 || processes.calls != 1 {
		t.Fatalf("process drain verification = exists %d terminate %d, want 3 and 1", processes.existsCalls, processes.calls)
	}
}

func TestReleaseServiceRetainsOwnershipWhenGracefulTreeSurvives(t *testing.T) {
	store, service, assignmentID, _ := newReleaseFixture(t, nil)
	addTrackedProcess(t, store, assignmentID)
	processes := &releaseProcesses{exists: []bool{true}, keepAlive: true}
	release := lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: processes, Git: &releaseGit{}, Ports: &releasePorts{}, Locker: &releaseLocker{}, LocksRoot: t.TempDir(),
		ProcessDrainTimeout: 2 * time.Millisecond, ProcessPollInterval: time.Millisecond,
	})

	_, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{})
	if err == nil || !strings.Contains(err.Error(), "retry with --force") {
		t.Fatalf("Release error = %v, want bounded graceful-survival error", err)
	}
	if processes.calls != 1 || assignmentFromStore(t, store, assignmentID).Lifecycle != state.LifecycleAssigned {
		t.Fatalf("surviving process cleanup = calls %d workspace %#v", processes.calls, assignmentFromStore(t, store, assignmentID))
	}
}

func (processes *releaseProcesses) Terminate(_ context.Context, _ state.TrackedProcessRecord, force bool) (bool, error) {
	processes.calls++
	processes.terminate = append(processes.terminate, force)
	return true, nil
}

type releaseGit struct {
	err         error
	relocked    int
	paths       []string
	projections []string
}

func (git *releaseGit) ResetCleanReturn(_ context.Context, path string, _ bool, projections []string) error {
	git.paths = append(git.paths, path)
	git.projections = append([]string(nil), projections...)
	return git.err
}

func (git *releaseGit) Lock(context.Context, string) error {
	git.relocked++
	return nil
}

type releasePorts struct {
	err   error
	calls int
}

func (ports *releasePorts) Release(context.Context, string) error {
	ports.calls++
	return ports.err
}

type releaseLocker struct {
	calls  int
	path   string
	inside func()
}

func (locker *releaseLocker) With(_ context.Context, path string, callback func() error) error {
	locker.calls++
	locker.path = path
	if locker.inside != nil {
		locker.inside()
	}
	return callback()
}

func TestReleaseServiceCleanRelease(t *testing.T) {
	store, service, assignmentID, path := newReleaseFixture(t, nil)
	addTrackedProcess(t, store, assignmentID)
	processes := &releaseProcesses{exists: []bool{true, false}}
	git := &releaseGit{}
	ports := &releasePorts{}
	locker := &releaseLocker{}
	release := lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: processes, Git: git, Ports: ports, Locker: locker, LocksRoot: filepath.Join(t.TempDir(), "locks"),
	})

	result, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{})
	if err != nil {
		t.Fatalf("Release returned an error: %v", err)
	}
	if result.Workspace.Lifecycle != state.LifecycleAvailable || result.Workspace.Assignment != nil {
		t.Fatalf("released workspace = %#v", result.Workspace)
	}
	if result.CleanedProcesses != 1 || processes.calls != 1 || ports.calls != 1 || len(git.paths) != 1 || git.paths[0] != path {
		t.Fatalf("cleanup calls = result %#v, processes %d, ports %d, git %#v", result, processes.calls, ports.calls, git.paths)
	}
	if locker.calls != 1 || !strings.Contains(locker.path, "workspace-") {
		t.Fatalf("lock calls = %d path %q", locker.calls, locker.path)
	}
}

func TestReleaseServiceReadsPreservedProjectionsInsideWorkspaceFence(t *testing.T) {
	store, service, assignmentID, path := newReleaseFixture(t, nil)
	git := &releaseGit{}
	locker := &releaseLocker{}
	readerCalled := false
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	locker.inside = func() {
		tree := store.current.Trees[key]
		tree.Projections = []string{"packages/api/node_modules"}
		store.current.Trees[key] = tree
	}
	reader := func(ctx context.Context, workspace state.WorkspaceRecord) ([]string, error) {
		readerCalled = true
		if locker.calls != 1 || locker.path == "" {
			t.Fatalf("projection reader ran outside workspace fence: calls=%d path=%q", locker.calls, locker.path)
		}
		if workspace.Path != path {
			t.Fatalf("projection reader workspace = %q, want %q", workspace.Path, path)
		}
		snapshot, readErr := store.Read(ctx)
		if readErr != nil {
			return nil, readErr
		}
		return append([]string(nil), snapshot.Trees[key].Projections...), nil
	}
	release := lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: &releaseProcesses{}, Git: git, Ports: &releasePorts{}, Locker: locker,
		LocksRoot: t.TempDir(),
	})

	if _, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{PreservedProjectionReader: reader}); err != nil {
		t.Fatalf("Release returned an error: %v", err)
	}
	if !readerCalled || len(git.projections) != 1 || git.projections[0] != "packages/api/node_modules" {
		t.Fatalf("projection reader/git inputs = %v/%v, want fenced reader and current projection", readerCalled, git.projections)
	}
}

func TestReleaseServiceDirtyNonForceRetainsOwnership(t *testing.T) {
	store, service, assignmentID, _ := newReleaseFixture(t, nil)
	release := newReleaseService(service, store, &releaseProcesses{}, &releaseGit{err: errors.New("Workspace has uncommitted changes")}, &releasePorts{})

	_, err := release.ReleaseAssignment(context.Background(), assignmentID, lifecycle.ReleaseOptions{})
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want dirty-workspace error", err)
	}
	workspace := assignmentFromStore(t, store, assignmentID)
	if workspace.Lifecycle != state.LifecycleAssigned || workspace.Assignment == nil || workspace.Failure == nil {
		t.Fatalf("dirty release did not retain ownership: %#v", workspace)
	}
}

func TestReleaseServiceProcessIdentityUncertaintyRetainsOwnership(t *testing.T) {
	store, service, assignmentID, _ := newReleaseFixture(t, nil)
	addTrackedProcess(t, store, assignmentID)
	git := &releaseGit{}
	release := newReleaseService(service, store, &releaseProcesses{err: errors.New("identity unavailable")}, git, &releasePorts{})

	_, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{})
	if err == nil || !strings.Contains(err.Error(), "identity unavailable") {
		t.Fatalf("error = %v, want identity uncertainty", err)
	}
	if assignmentFromStore(t, store, assignmentID).Lifecycle != state.LifecycleAssigned || len(git.paths) != 0 {
		t.Fatalf("identity uncertainty was not fail-closed: %#v git=%#v", assignmentFromStore(t, store, assignmentID), git.paths)
	}
}

func TestReleaseServiceGitFailureRollsBackAndRelocks(t *testing.T) {
	store, service, assignmentID, path := newReleaseFixture(t, nil)
	git := &releaseGit{err: errors.New("git clean failed")}
	release := newReleaseService(service, store, &releaseProcesses{}, git, &releasePorts{})

	_, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{})
	if err == nil || !strings.Contains(err.Error(), "git clean failed") {
		t.Fatalf("error = %v, want Git error", err)
	}
	if git.relocked != 1 {
		t.Fatalf("relock calls = %d, want 1", git.relocked)
	}
	if len(git.paths) != 1 || git.paths[0] != path || assignmentFromStore(t, store, assignmentID).Lifecycle != state.LifecycleAssigned {
		t.Fatalf("Git failure rollback was incomplete: paths=%#v workspace=%#v", git.paths, assignmentFromStore(t, store, assignmentID))
	}
}

func TestReleaseServicePortFailureDoesNotRollbackAvailableState(t *testing.T) {
	store, service, assignmentID, path := newReleaseFixture(t, nil)
	ports := &releasePorts{err: errors.New("port registry unavailable")}
	git := &releaseGit{}
	release := newReleaseService(service, store, &releaseProcesses{}, git, ports)

	if _, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{}); err != nil {
		t.Fatalf("port cleanup error made release fail: %v", err)
	}
	workspace := workspaceAtPath(t, store, path)
	if ports.calls != 1 || workspace.Lifecycle != state.LifecycleAvailable {
		t.Fatalf("port failure changed publication: calls=%d workspace=%#v", ports.calls, workspace)
	}
	if workspace.Assignment != nil {
		t.Fatal("port failure restored a stale assignment")
	}
	ports.err = nil
	if _, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{}); err == nil {
		t.Fatal("released workspace with stale assignment unexpectedly remained releasable")
	}
	if ports.calls != 1 {
		t.Fatalf("port cleanup was retried against available state: %d", ports.calls)
	}
}

func TestReleaseServiceFinishFailureSkipsPortCleanup(t *testing.T) {
	store, service, assignmentID, _ := newReleaseFixture(t, nil)
	store.failFinish = errors.New("finish publication failed")
	ports := &releasePorts{}
	release := newReleaseService(service, store, &releaseProcesses{}, &releaseGit{}, ports)

	if _, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{}); err == nil || !strings.Contains(err.Error(), "finish publication failed") {
		t.Fatalf("finish failure = %v", err)
	}
	if ports.calls != 0 {
		t.Fatalf("port cleanup ran after finish failure: %d", ports.calls)
	}
	if assignmentFromStore(t, store, assignmentID).Lifecycle != state.LifecycleAssigned {
		t.Fatal("finish failure did not restore assignment ownership")
	}
}

func TestReleaseServiceAcquisitionInProgressIsRetryableConflict(t *testing.T) {
	operation := "acquire-operation"
	store, service, assignmentID, _ := newReleaseFixture(t, &operation)
	locker := &releaseLocker{}
	release := lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: &releaseProcesses{}, Git: &releaseGit{}, Ports: &releasePorts{}, Locker: locker, LocksRoot: t.TempDir(),
	})

	_, err := release.Release(context.Background(), assignmentID, lifecycle.ReleaseOptions{})
	var conflict *lifecycle.AcquisitionInProgressError
	if !errors.As(err, &conflict) || !conflict.Retryable() {
		t.Fatalf("error = %v, want retryable acquisition conflict", err)
	}
	if locker.calls != 1 {
		t.Fatalf("conflicting release lock calls = %d, want one fenced check", locker.calls)
	}
}

func newReleaseService(service *lifecycle.Service, store *releaseStore, processes lifecycle.ReleaseProcesser, git lifecycle.ReleaseGitter, ports lifecycle.ReleasePorter) *lifecycle.ReleaseService {
	return lifecycle.NewReleaseService(service, lifecycle.ReleaseServiceOptions{
		Reader: store, Processes: processes, Git: git, Ports: ports, Locker: &releaseLocker{}, LocksRoot: filepath.Join("test", "locks"),
	})
}

func newReleaseFixture(t *testing.T, operationID *string) (*releaseStore, *lifecycle.Service, string, string) {
	t.Helper()
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "workspace")
	assignmentID := "assignment-release"
	workspaceKey, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &releaseStore{current: &state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{
		workspaceKey: {Path: path, Managed: true, Branch: "agent/release", Lifecycle: state.LifecycleAssigned, OperationID: operationID, Assignment: &state.AssignmentRecord{ID: assignmentID, Owner: "agent", Hostname: "host", AssignedAt: now.Format(time.RFC3339Nano), RenewedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), LastActivityAt: now.Format(time.RFC3339Nano), LeaseKeepers: []state.LeaseKeeperRecord{}, Ports: map[string]int64{}}, Processes: []state.TrackedProcessRecord{}, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano)},
	}, Metrics: state.EmptyMetrics()}}
	service := lifecycle.New(store, lifecycle.Options{Now: func() time.Time { return now }, NewID: func() string { return "unused" }})
	return store, service, assignmentID, path
}

func addTrackedProcess(t *testing.T, store *releaseStore, assignmentID string) {
	t.Helper()
	for key, workspace := range store.current.Workspaces {
		if workspace.Assignment != nil && workspace.Assignment.ID == assignmentID {
			workspace.Processes = []state.TrackedProcessRecord{{PID: 42, StartedAt: "identity"}}
			store.current.Workspaces[key] = workspace
			return
		}
	}
	t.Fatalf("assignment %s disappeared", assignmentID)
}

func assignmentFromStore(t *testing.T, store *releaseStore, assignmentID string) state.WorkspaceRecord {
	t.Helper()
	for _, workspace := range store.current.Workspaces {
		if workspace.Assignment != nil && workspace.Assignment.ID == assignmentID {
			return workspace
		}
	}
	t.Fatalf("assignment %s disappeared", assignmentID)
	return state.WorkspaceRecord{}
}

func workspaceAtPath(t *testing.T, store *releaseStore, path string) state.WorkspaceRecord {
	t.Helper()
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := store.current.Workspaces[key]
	if !ok {
		t.Fatalf("workspace %s disappeared", path)
	}
	return workspace
}
