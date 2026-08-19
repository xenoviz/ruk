package lifecycle_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

func TestWorkspaceCollectionIsOperationAndUpdateFenced(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	service := lifecycle.New(store, lifecycle.Options{
		Now:   func() time.Time { return now },
		NewID: func() string { return "collection-operation" },
	})
	workspacePath := t.TempDir()
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	availableAt := "2026-01-01T01:00:00.000Z"
	store.current.Workspaces[key] = state.WorkspaceRecord{
		Path: workspacePath, Managed: true, Branch: "agent/test", Lifecycle: state.LifecycleAvailable,
		Processes: []state.TrackedProcessRecord{}, CreatedAt: availableAt, UpdatedAt: availableAt, AvailableAt: &availableAt,
	}

	_, err = service.BeginWorkspaceCollection(context.Background(), workspacePath, "stale")
	if err == nil || !strings.Contains(err.Error(), "changed before collection") {
		t.Fatalf("stale update fence error = %v", err)
	}

	collecting, err := service.BeginWorkspaceCollection(context.Background(), workspacePath, availableAt)
	if err != nil {
		t.Fatalf("BeginWorkspaceCollection returned an error: %v", err)
	}
	if collecting.OperationID == nil || *collecting.OperationID != "collection-operation" {
		t.Fatalf("collecting workspace = %#v", collecting)
	}

	now = now.Add(time.Minute)
	cancelled, err := service.CancelWorkspaceCollection(context.Background(), workspacePath, "collection-operation")
	if err != nil {
		t.Fatalf("CancelWorkspaceCollection returned an error: %v", err)
	}
	if cancelled.OperationID != nil || cancelled.AvailableAt == nil || *cancelled.AvailableAt != "2026-01-01T02:01:00.000Z" {
		t.Fatalf("cancelled workspace = %#v", cancelled)
	}

	now = now.Add(time.Minute)
	collecting, err = service.BeginWorkspaceCollection(context.Background(), workspacePath, cancelled.UpdatedAt)
	if err != nil {
		t.Fatalf("second BeginWorkspaceCollection returned an error: %v", err)
	}
	deleted, err := service.DeleteWorkspaceRecord(context.Background(), workspacePath, *collecting.OperationID)
	if err != nil {
		t.Fatalf("DeleteWorkspaceRecord returned an error: %v", err)
	}
	if deleted.Path != workspacePath || len(store.current.Workspaces) != 0 {
		t.Fatalf("deleted=%#v remaining=%#v", deleted, store.current.Workspaces)
	}
}

func TestAbandonedPreparationBecomesFailedBeforeCollection(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	service := lifecycle.New(store, lifecycle.Options{
		Now:   func() time.Time { return now },
		NewID: func() string { return "collection-operation" },
	})
	workspacePath := t.TempDir()
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	preparationOperation := "preparation-operation"
	updatedAt := "2026-01-01T01:00:00.000Z"
	store.current.Workspaces[key] = state.WorkspaceRecord{
		Path: workspacePath, Managed: true, Branch: "agent/test", Lifecycle: state.LifecyclePreparing,
		OperationID: &preparationOperation, Processes: []state.TrackedProcessRecord{}, CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}

	collecting, err := service.BeginWorkspaceCollection(context.Background(), workspacePath, updatedAt)
	if err != nil {
		t.Fatalf("BeginWorkspaceCollection returned an error: %v", err)
	}
	if collecting.Lifecycle != state.LifecycleFailed || collecting.Failure == nil || *collecting.Failure != "Workspace preparation was abandoned" {
		t.Fatalf("collecting workspace = %#v", collecting)
	}
}

func TestWorkspaceCollectionRefusesTrackedProcessesBeforePublishingFence(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service := lifecycle.New(store, lifecycle.Options{
		Now:   func() time.Time { return time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC) },
		NewID: func() string { return "collection-operation" },
	})
	workspacePath := t.TempDir()
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := "2026-01-01T01:00:00.000Z"
	store.current.Workspaces[key] = state.WorkspaceRecord{
		Path: workspacePath, Managed: true, Branch: "agent/test", Lifecycle: state.LifecycleFailed,
		Processes: []state.TrackedProcessRecord{{PID: 42, StartedAt: "native:42"}}, CreatedAt: updatedAt, UpdatedAt: updatedAt,
		Failure: collectionFailurePointer("installer cleanup is unresolved"),
	}

	if _, err := service.BeginWorkspaceCollection(context.Background(), workspacePath, updatedAt); err == nil || !strings.Contains(err.Error(), "tracked processes") {
		t.Fatalf("BeginWorkspaceCollection error = %v, want tracked-process refusal", err)
	}
	if store.current.Workspaces[key].OperationID != nil {
		t.Fatalf("collection fence was published despite tracked processes: %#v", store.current.Workspaces[key])
	}
}

func TestMarkFailedRetainsTrackedPreparationProcess(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service := lifecycle.New(store, lifecycle.Options{
		Now:   func() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) },
		NewID: func() string { return preparationID },
	})
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.BeginPreparation(context.Background(), workspacePath, "agent/test"); err != nil {
		t.Fatalf("BeginPreparation returned an error: %v", err)
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	workspace := store.current.Workspaces[key]
	workspace.Processes = []state.TrackedProcessRecord{{PID: 42, StartedAt: "native:42"}}
	store.current.Workspaces[key] = workspace

	failed, err := service.MarkFailed(context.Background(), workspacePath, preparationID, "installer cleanup is unresolved")
	if err != nil {
		t.Fatalf("MarkFailed returned an error: %v", err)
	}
	if failed.Lifecycle != state.LifecycleFailed || len(failed.Processes) != 1 || failed.Processes[0].PID != 42 {
		t.Fatalf("failed workspace = %#v, want tracked process retained", failed)
	}
}

func TestMarkAvailableRefusesTrackedPreparationProcess(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	service := lifecycle.New(store, lifecycle.Options{
		Now:   func() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) },
		NewID: func() string { return preparationID },
	})
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.BeginPreparation(context.Background(), workspacePath, "agent/test"); err != nil {
		t.Fatalf("BeginPreparation returned an error: %v", err)
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	workspace := store.current.Workspaces[key]
	workspace.Processes = []state.TrackedProcessRecord{{PID: 42, StartedAt: "native:42"}}
	store.current.Workspaces[key] = workspace

	if _, err := service.MarkAvailable(context.Background(), workspacePath, preparationID); err == nil || !strings.Contains(err.Error(), "tracked processes") {
		t.Fatalf("MarkAvailable error = %v, want tracked-process refusal", err)
	}
	if store.current.Workspaces[key].Lifecycle != state.LifecyclePreparing {
		t.Fatalf("workspace lifecycle = %s, want preparing", store.current.Workspaces[key].Lifecycle)
	}
}

func collectionFailurePointer(value string) *string { return &value }
