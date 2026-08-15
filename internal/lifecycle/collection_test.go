package lifecycle_test

import (
	"context"
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
