package lifecycle_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

type acquisitionLockFake struct {
	enter func()
}

type acquisitionStateReader struct {
	store *memoryStore
}

func (reader acquisitionStateReader) Read(context.Context) (*state.State, error) {
	return reader.store.current, nil
}

func (lock acquisitionLockFake) With(_ context.Context, _ string, callback func() error) error {
	if lock.enter != nil {
		lock.enter()
	}
	return callback()
}

type acquisitionWorktreeFake struct {
	created  int
	assigned int
}

func (worktree *acquisitionWorktreeFake) Create(context.Context, string, string, string) error {
	worktree.created++
	return nil
}

func (worktree *acquisitionWorktreeFake) Assign(context.Context, string, string, string) error {
	worktree.assigned++
	return nil
}

type acquisitionPortsFake struct {
	err error
}

func (ports acquisitionPortsFake) Allocate(context.Context, string, []string) (state.WorkspaceRecord, error) {
	return state.WorkspaceRecord{}, ports.err
}

func acquisitionTestService(t *testing.T, store *memoryStore, ids []string, worktree *acquisitionWorktreeFake, prepare lifecycle.DependencyPreparer, ports lifecycle.PortAllocator, lock lifecycle.AcquisitionLocker, cleanup func(context.Context, string, bool) error) *lifecycle.AcquisitionService {
	t.Helper()
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	service := lifecycle.New(store, lifecycle.Options{
		Now: func() time.Time { return now },
		NewID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
	return lifecycle.NewAcquisitionService(lifecycle.AcquisitionOptions{
		Lifecycle: service,
		Reader:    acquisitionStateReader{store: store},
		Locker:    lock,
		Worktree:  worktree,
		Prepare:   prepare,
		Ports:     ports,
		Cleanup:   cleanup,
	})
}

func successfulDependencies(context.Context, string) (dependencies.EnsureResult, error) {
	return dependencies.EnsureResult{Fingerprint: "fingerprint", Mode: "managed-install"}, nil
}

func TestAcquireReusesAvailableWorkspaceAndHoldsHandoffFence(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	path := addAvailableWorkspace(t, store, "reuse", "2026-01-01T00:00:00.000Z", nil, state.LifecycleAvailable)
	worktree := &acquisitionWorktreeFake{}
	lockEnteredBeforeReserve := false
	lock := acquisitionLockFake{enter: func() {
		key, _ := state.TreeKey(path)
		workspace := store.current.Workspaces[key]
		lockEnteredBeforeReserve = workspace.Lifecycle == state.LifecycleAvailable && workspace.OperationID == nil
	}}
	service := acquisitionTestService(t, store, []string{assignmentID, acquisitionID}, worktree, successfulDependencies, acquisitionPortsFake{}, lock, nil)

	result, err := service.Acquire(context.Background(), lifecycle.AcquireInput{
		Assignment: lifecycle.AssignmentInput{Owner: "agent", Hostname: "host", ExpiresAt: time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC)},
		Branch:     "agent/reuse",
		StartPoint: "HEAD",
	})
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	if !result.Reused || result.AssignmentID != assignmentID || result.Path != path || result.Fingerprint != "fingerprint" {
		t.Fatalf("result = %#v", result)
	}
	if worktree.assigned != 1 || worktree.created != 0 {
		t.Fatalf("worktree calls = %#v", worktree)
	}
	if !lockEnteredBeforeReserve {
		t.Fatal("available workspace was published before its acquisition lock was held")
	}
	key, _ := state.TreeKey(path)
	if workspace := store.current.Workspaces[key]; workspace.OperationID != nil || workspace.Lifecycle != state.LifecycleAssigned {
		t.Fatalf("completed workspace = %#v", workspace)
	}
}

func TestAcquireCreatesPreparingWorkspaceAndAssignsAfterPreparation(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	path := filepath.Join(t.TempDir(), "fresh")
	worktree := &acquisitionWorktreeFake{}
	lockEnteredBeforePreparation := false
	lock := acquisitionLockFake{enter: func() {
		key, _ := state.TreeKey(path)
		_, exists := store.current.Workspaces[key]
		lockEnteredBeforePreparation = !exists
	}}
	service := acquisitionTestService(t, store, []string{preparationID, assignmentID, acquisitionID}, worktree, successfulDependencies, acquisitionPortsFake{}, lock, nil)

	result, err := service.Acquire(context.Background(), lifecycle.AcquireInput{
		Assignment:    lifecycle.AssignmentInput{Owner: "agent", Hostname: "host", ExpiresAt: time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC)},
		Branch:        "agent/fresh",
		StartPoint:    "origin/main",
		WorkspacePath: path,
	})
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	if result.Reused || result.AssignmentID != assignmentID || worktree.created != 1 || worktree.assigned != 0 {
		t.Fatalf("result/worktree = %#v / %#v", result, worktree)
	}
	if !lockEnteredBeforePreparation {
		t.Fatal("preparing workspace was published before its acquisition lock was held")
	}
	key, _ := state.TreeKey(path)
	workspace := store.current.Workspaces[key]
	if workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil || workspace.Branch != "agent/fresh" {
		t.Fatalf("fresh workspace = %#v", workspace)
	}
}

func TestAcquirePreparationFailureRecordsFailedWorkspace(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	path := filepath.Join(t.TempDir(), "failed-preparation")
	worktree := &acquisitionWorktreeFake{}
	prepareErr := errors.New("installer failed")
	service := acquisitionTestService(t, store, []string{preparationID}, worktree, func(context.Context, string) (dependencies.EnsureResult, error) {
		return dependencies.EnsureResult{}, prepareErr
	}, acquisitionPortsFake{}, acquisitionLockFake{}, nil)

	_, err := service.Acquire(context.Background(), lifecycle.AcquireInput{
		Assignment:    lifecycle.AssignmentInput{Owner: "agent", Hostname: "host", ExpiresAt: time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC)},
		Branch:        "agent/failed",
		WorkspacePath: path,
	})
	if err == nil || !strings.Contains(err.Error(), prepareErr.Error()) {
		t.Fatalf("Acquire error = %v", err)
	}
	key, _ := state.TreeKey(path)
	workspace := store.current.Workspaces[key]
	if workspace.Lifecycle != state.LifecycleFailed || workspace.OperationID != nil || workspace.Failure == nil {
		t.Fatalf("failed workspace = %#v", workspace)
	}
}

func TestAcquireCleanupFailureRetainsReusableAssignmentForRecovery(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	path := addAvailableWorkspace(t, store, "retained", "2026-01-01T00:00:00.000Z", nil, state.LifecycleAvailable)
	cleanupErr := errors.New("cleanup could not verify tree")
	service := acquisitionTestService(t, store, []string{assignmentID, acquisitionID}, &acquisitionWorktreeFake{}, func(context.Context, string) (dependencies.EnsureResult, error) {
		return dependencies.EnsureResult{}, errors.New("dependency failed")
	}, acquisitionPortsFake{}, acquisitionLockFake{}, func(context.Context, string, bool) error { return cleanupErr })

	_, err := service.Acquire(context.Background(), lifecycle.AcquireInput{
		Assignment: lifecycle.AssignmentInput{Owner: "agent", Hostname: "host", ExpiresAt: time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC)},
		Branch:     "agent/retained",
	})
	if err == nil || !strings.Contains(err.Error(), cleanupErr.Error()) {
		t.Fatalf("Acquire error = %v", err)
	}
	key, _ := state.TreeKey(path)
	workspace := store.current.Workspaces[key]
	if workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil || workspace.Failure == nil {
		t.Fatalf("retained workspace = %#v", workspace)
	}
	if _, releaseErr := lifecycle.New(store, lifecycle.Options{Now: time.Now, NewID: func() string { return "unused" }}).BeginWorkspaceReturn(context.Background(), assignmentID); releaseErr != nil {
		t.Fatalf("retained assignment is not recoverable: %v", releaseErr)
	}
}

func TestAcquireRejectsConcurrentOwnershipChangeAndDoesNotTouchWorktree(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	path := addAvailableWorkspace(t, store, "concurrent", "2026-01-01T00:00:00.000Z", nil, state.LifecycleAvailable)
	worktree := &acquisitionWorktreeFake{}
	lock := acquisitionLockFake{enter: func() {
		key, _ := state.TreeKey(path)
		operation := "new-owner-operation"
		workspace := store.current.Workspaces[key]
		workspace.OperationID = &operation
		store.current.Workspaces[key] = workspace
	}}
	service := acquisitionTestService(t, store, []string{assignmentID, acquisitionID}, worktree, successfulDependencies, acquisitionPortsFake{}, lock, nil)

	_, err := service.Acquire(context.Background(), lifecycle.AcquireInput{
		Assignment: lifecycle.AssignmentInput{Owner: "agent", Hostname: "host", ExpiresAt: time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC)},
		Branch:     "agent/concurrent",
	})
	if err == nil || !strings.Contains(err.Error(), "Acquisition operation does not match") {
		t.Fatalf("Acquire error = %v", err)
	}
	if worktree.assigned != 0 {
		t.Fatalf("worktree was touched after ownership changed: %#v", worktree)
	}
}

func TestAcquirePortsFailureRetainsAssignmentWithoutPublishingSuccess(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	path := addAvailableWorkspace(t, store, "ports-failure", "2026-01-01T00:00:00.000Z", nil, state.LifecycleAvailable)
	portErr := errors.New("no named ports available")
	service := acquisitionTestService(t, store, []string{assignmentID, acquisitionID}, &acquisitionWorktreeFake{}, successfulDependencies, acquisitionPortsFake{err: portErr}, acquisitionLockFake{}, nil)

	_, err := service.Acquire(context.Background(), lifecycle.AcquireInput{
		Assignment: lifecycle.AssignmentInput{Owner: "agent", Hostname: "host", ExpiresAt: time.Date(2026, time.January, 1, 8, 0, 0, 0, time.UTC)},
		Branch:     "agent/ports",
		PortNames:  []string{"app"},
	})
	if err == nil || !strings.Contains(err.Error(), portErr.Error()) {
		t.Fatalf("Acquire error = %v", err)
	}
	key, _ := state.TreeKey(path)
	workspace := store.current.Workspaces[key]
	if workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil || workspace.Assignment == nil {
		t.Fatalf("port failure state = %#v", workspace)
	}
}
