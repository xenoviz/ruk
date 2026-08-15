package lifecycle_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

type gcServiceStore struct {
	current    *state.State
	failDelete bool
}

func (store *gcServiceStore) Update(_ context.Context, mutate func(*state.State) error) error {
	if store.failDelete {
		for _, workspace := range store.current.Workspaces {
			if workspace.Lifecycle == state.LifecycleAvailable && workspace.OperationID != nil {
				return errors.New("delete workspace record failed")
			}
		}
	}
	return mutate(store.current)
}

func (store *gcServiceStore) Read(context.Context) (*state.State, error) { return store.current, nil }

type gcTestLocker struct{ paths []string }

func (locker *gcTestLocker) With(_ context.Context, path string, callback func() error) error {
	locker.paths = append(locker.paths, path)
	return callback()
}

type gcTestGit struct {
	worktree  bool
	unlockErr error
	removeErr error
	lockErr   error
	unlock    int
	remove    int
	lock      int
}

func (git *gcTestGit) IsWorktree(context.Context, string) (bool, error) { return git.worktree, nil }
func (git *gcTestGit) Unlock(context.Context, string) error {
	git.unlock++
	return git.unlockErr
}
func (git *gcTestGit) Remove(context.Context, string, bool) error {
	git.remove++
	return git.removeErr
}
func (git *gcTestGit) Lock(context.Context, string) error {
	git.lock++
	return git.lockErr
}

type gcTestTreeState struct {
	calls int
	err   error
}

func (tree *gcTestTreeState) DeleteTreeState(context.Context, string) error {
	tree.calls++
	return tree.err
}

type gcTestRelease struct {
	store   *gcServiceStore
	options []lifecycle.ReleaseOptions
	now     time.Time
}

func (release *gcTestRelease) ReleaseAssignment(_ context.Context, assignmentID string, options lifecycle.ReleaseOptions) (lifecycle.ReleaseResult, error) {
	release.options = append(release.options, options)
	for key, workspace := range release.store.current.Workspaces {
		if workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
			continue
		}
		updated := release.now.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
		workspace.Lifecycle = state.LifecycleAvailable
		workspace.OperationID = nil
		workspace.Assignment = nil
		workspace.AvailableAt = &updated
		workspace.UpdatedAt = updated
		release.store.current.Workspaces[key] = workspace
		return lifecycle.ReleaseResult{Workspace: workspace}, nil
	}
	return lifecycle.ReleaseResult{}, errors.New("assignment does not exist")
}

func TestGCServicePlanAndForcePolicy(t *testing.T) {
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	store, service := gcFixture(t, now, gcWorkspaceForService("/pool/old", state.LifecycleAvailable, nil, nil, "2026-01-01T00:00:00.000Z"), gcWorkspaceForService("/pool/expired", state.LifecycleAssigned, nil, gcAssignment("expired", "2026-01-01T01:00:00.000Z", nil), "2026-01-01T00:00:00.000Z"))
	locker := &gcTestLocker{}
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{Reader: store, Lifecycle: service, Locker: locker, LocksRoot: t.TempDir()})

	result, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: time.Date(2026, time.January, 1, 1, 0, 0, 0, time.UTC), Now: now, CurrentWorkspacePath: "/pool/old"})
	if err != nil {
		t.Fatalf("plan returned an error: %v", err)
	}
	if result.Status != "planned" || len(result.Removed) != 0 || len(result.Expired) != 1 || len(locker.paths) != 2 {
		t.Fatalf("plan = %#v locks=%#v", result, locker.paths)
	}
	if _, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: now.Add(-time.Hour), Now: now, ForceExpired: true}); err == nil || !strings.Contains(err.Error(), "requires --apply") {
		t.Fatalf("force-expired policy error = %v", err)
	}
}

func TestGCServiceApplyRemovesSafeWorkspaceAndSkipsCurrent(t *testing.T) {
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	store, service := gcFixture(t, now, gcWorkspaceForService("/pool/old", state.LifecycleAvailable, nil, nil, "2026-01-01T00:00:00.000Z"), gcWorkspaceForService("/pool/current", state.LifecycleAvailable, nil, nil, "2026-01-01T00:00:00.000Z"))
	git := &gcTestGit{worktree: true}
	tree := &gcTestTreeState{}
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{Reader: store, Lifecycle: service, Git: git, TreeState: tree, Locker: &gcTestLocker{}, LocksRoot: t.TempDir()})

	result, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: now.Add(-time.Hour), Now: now, Apply: true, CurrentWorkspacePath: "/pool/current"})
	if err != nil {
		t.Fatalf("apply returned an error: %v", err)
	}
	if result.Status != "collected" || len(result.Removed) != 1 || result.Removed[0].Path != "/pool/old" {
		t.Fatalf("apply result = %#v", result)
	}
	if git.unlock != 1 || git.remove != 1 || tree.calls != 1 {
		t.Fatalf("Git/tree calls = unlock %d remove %d tree %d", git.unlock, git.remove, tree.calls)
	}
	if len(store.current.Workspaces) != 1 {
		t.Fatalf("current workspace was not preserved: %#v", store.current.Workspaces)
	}
}

func TestGCServiceAbandonedAcquisitionUsesReleaseFence(t *testing.T) {
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	operation := "acquisition-operation"
	workspace := gcWorkspaceForService("/pool/acquiring", state.LifecycleAssigned, &operation, gcAssignment("assignment", "2026-01-01T08:00:00.000Z", nil), "2026-01-01T00:00:00.000Z")
	store, service := gcFixture(t, now, workspace)
	release := &gcTestRelease{store: store, now: now}
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{Reader: store, Lifecycle: service, Release: release, Git: &gcTestGit{}, TreeState: &gcTestTreeState{}, Locker: &gcTestLocker{}, LocksRoot: t.TempDir()})

	result, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: now.Add(-time.Hour), Now: now, Apply: true})
	if err != nil {
		t.Fatalf("abandoned acquisition apply returned an error: %v", err)
	}
	if len(release.options) != 1 || !release.options[0].Force || release.options[0].AcquisitionOperationID != operation || release.options[0].ExpectedUpdatedAt != workspace.UpdatedAt {
		t.Fatalf("release options = %#v", release.options)
	}
	if len(result.Removed) != 1 || len(store.current.Workspaces) != 0 {
		t.Fatalf("abandoned acquisition result/state = %#v/%#v", result, store.current.Workspaces)
	}
}

func TestGCServiceForcedExpiryRecomputesExpiredOutput(t *testing.T) {
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	workspace := gcWorkspaceForService("/pool/expired", state.LifecycleAssigned, nil, gcAssignment("expired", "2026-01-01T01:00:00.000Z", nil), "2026-01-01T00:00:00.000Z")
	store, service := gcFixture(t, now, workspace)
	release := &gcTestRelease{store: store, now: now}
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{Reader: store, Lifecycle: service, Release: release, Git: &gcTestGit{}, TreeState: &gcTestTreeState{}, Locker: &gcTestLocker{}, LocksRoot: t.TempDir()})

	result, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: now.Add(-time.Hour), Now: now, Apply: true, ForceExpired: true})
	if err != nil {
		t.Fatalf("forced expiry returned an error: %v", err)
	}
	if len(release.options) != 1 || !release.options[0].Force || release.options[0].RequireExpiredBy == "" || len(result.Expired) != 0 {
		t.Fatalf("forced expiry release/result = %#v/%#v", release.options, result)
	}
}

func TestGCServiceRemoveFailureRelocksAndRestoresCollection(t *testing.T) {
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	store, service := gcFixture(t, now, gcWorkspaceForService("/pool/remove", state.LifecycleAvailable, nil, nil, "2026-01-01T00:00:00.000Z"))
	git := &gcTestGit{worktree: true, removeErr: errors.New("remove failed")}
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{Reader: store, Lifecycle: service, Git: git, TreeState: &gcTestTreeState{}, Locker: &gcTestLocker{}, LocksRoot: t.TempDir()})

	_, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: now.Add(-time.Hour), Now: now, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "remove failed") || git.lock != 1 {
		t.Fatalf("remove failure = %v, relock calls=%d", err, git.lock)
	}
	workspace := workspaceAtPathForGC(t, store, "/pool/remove")
	if workspace.OperationID != nil || workspace.Lifecycle != state.LifecycleAvailable {
		t.Fatalf("collection was not restored: %#v", workspace)
	}
}

func TestGCServiceRelockFailureLeavesRetryableCollectionFence(t *testing.T) {
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	store, service := gcFixture(t, now, gcWorkspaceForService("/pool/relock", state.LifecycleAvailable, nil, nil, "2026-01-01T00:00:00.000Z"))
	git := &gcTestGit{worktree: true, removeErr: errors.New("remove failed"), lockErr: errors.New("relock failed")}
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{Reader: store, Lifecycle: service, Git: git, TreeState: &gcTestTreeState{}, Locker: &gcTestLocker{}, LocksRoot: t.TempDir()})

	_, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: now.Add(-time.Hour), Now: now, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "relock failed") {
		t.Fatalf("relock failure = %v", err)
	}
	if workspaceAtPathForGC(t, store, "/pool/relock").OperationID == nil {
		t.Fatal("failed relock lost retryable collection fence")
	}
}

func TestGCServicePostRemovalStateFailureRetainsRetryFence(t *testing.T) {
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	store, service := gcFixture(t, now, gcWorkspaceForService("/pool/state", state.LifecycleAvailable, nil, nil, "2026-01-01T00:00:00.000Z"))
	store.failDelete = true
	git := &gcTestGit{worktree: true}
	tree := &gcTestTreeState{}
	gc := lifecycle.NewGCService(lifecycle.GCServiceOptions{Reader: store, Lifecycle: service, Git: git, TreeState: tree, Locker: &gcTestLocker{}, LocksRoot: t.TempDir()})

	_, err := gc.Run(context.Background(), lifecycle.GCOptions{OlderThan: now.Add(-time.Hour), Now: now, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "delete workspace record failed") {
		t.Fatalf("state deletion failure = %v", err)
	}
	if workspaceAtPathForGC(t, store, "/pool/state").OperationID == nil || tree.calls != 0 {
		t.Fatalf("post-removal retry state/tree = %#v/%d", workspaceAtPathForGC(t, store, "/pool/state"), tree.calls)
	}
}

func gcFixture(t *testing.T, now time.Time, workspaces ...state.WorkspaceRecord) (*gcServiceStore, *lifecycle.Service) {
	t.Helper()
	store := &gcServiceStore{current: &state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{}, Metrics: state.EmptyMetrics()}}
	for _, workspace := range workspaces {
		key, err := state.TreeKey(workspace.Path)
		if err != nil {
			t.Fatal(err)
		}
		store.current.Workspaces[key] = workspace
	}
	ids := 0
	service := lifecycle.New(store, lifecycle.Options{Now: func() time.Time { return now }, NewID: func() string { ids++; return "collection-" + string(rune('0'+ids)) }})
	return store, service
}

func gcWorkspaceForService(path string, lifecycleState state.WorkspaceLifecycle, operationID *string, assignment *state.AssignmentRecord, updatedAt string) state.WorkspaceRecord {
	availableAt := updatedAt
	return state.WorkspaceRecord{Path: path, Managed: true, Branch: "agent/test", Lifecycle: lifecycleState, OperationID: operationID, Assignment: assignment, Processes: []state.TrackedProcessRecord{}, CreatedAt: updatedAt, UpdatedAt: updatedAt, AvailableAt: &availableAt}
}

func workspaceAtPathForGC(t *testing.T, store *gcServiceStore, path string) state.WorkspaceRecord {
	t.Helper()
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := store.current.Workspaces[key]
	if !ok {
		t.Fatalf("workspace %s is missing", path)
	}
	return workspace
}
