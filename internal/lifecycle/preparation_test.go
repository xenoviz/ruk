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

const preparationID = "46bc4998-95b0-4d16-b017-69b06a13747b"

type memoryStore struct {
	current *state.State
}

func newMemoryStore() *memoryStore {
	return &memoryStore{current: &state.State{
		Version:    state.CurrentVersion,
		Trees:      map[string]state.TreeRecord{},
		Workspaces: map[string]state.WorkspaceRecord{},
		Metrics:    state.EmptyMetrics(),
	}}
}

func (store *memoryStore) Update(ctx context.Context, mutate func(*state.State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return mutate(store.current)
}

func TestPreparationTransitionsAreOperationFenced(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	service := lifecycle.New(store, lifecycle.Options{
		Now:   func() time.Time { return now },
		NewID: func() string { return preparationID },
	})
	workspacePath := filepath.Join(t.TempDir(), "workspace")

	preparing, err := service.BeginPreparation(context.Background(), workspacePath, "agent/test")
	if err != nil {
		t.Fatalf("BeginPreparation returned an error: %v", err)
	}
	if preparing.Lifecycle != state.LifecyclePreparing || preparing.OperationID == nil || *preparing.OperationID != preparationID {
		t.Fatalf("preparing workspace = %#v", preparing)
	}
	if preparing.Path != workspacePath || !preparing.Managed || preparing.Branch != "agent/test" {
		t.Fatalf("preparing workspace identity = %#v", preparing)
	}
	if preparing.Processes == nil || len(preparing.Processes) != 0 {
		t.Fatalf("preparing processes = %#v, want empty non-nil slice", preparing.Processes)
	}
	if preparing.CreatedAt != "2026-01-01T00:00:00.000Z" || preparing.UpdatedAt != preparing.CreatedAt {
		t.Fatalf("preparing timestamps = %q / %q", preparing.CreatedAt, preparing.UpdatedAt)
	}

	_, err = service.MarkAvailable(context.Background(), workspacePath, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err == nil || !strings.Contains(err.Error(), "Preparation operation does not match") {
		t.Fatalf("MarkAvailable mismatch error = %v", err)
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	if store.current.Workspaces[key].Lifecycle != state.LifecyclePreparing {
		t.Fatalf("mismatched operation changed lifecycle to %s", store.current.Workspaces[key].Lifecycle)
	}

	now = now.Add(time.Minute)
	available, err := service.MarkAvailable(context.Background(), workspacePath, preparationID)
	if err != nil {
		t.Fatalf("MarkAvailable returned an error: %v", err)
	}
	if available.Lifecycle != state.LifecycleAvailable || available.OperationID != nil {
		t.Fatalf("available workspace = %#v", available)
	}
	if available.AvailableAt == nil || *available.AvailableAt != "2026-01-01T00:01:00.000Z" {
		t.Fatalf("AvailableAt = %#v", available.AvailableAt)
	}
}

func TestPreparationFailureRetainsReason(t *testing.T) {
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

	failed, err := service.MarkFailed(context.Background(), workspacePath, preparationID, "install failed")
	if err != nil {
		t.Fatalf("MarkFailed returned an error: %v", err)
	}
	if failed.Lifecycle != state.LifecycleFailed || failed.OperationID != nil {
		t.Fatalf("failed workspace = %#v", failed)
	}
	if failed.Failure == nil || *failed.Failure != "install failed" {
		t.Fatalf("Failure = %#v", failed.Failure)
	}
}

func TestPreparationFailureAtomicallyRetainsUnsafeProcess(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	service := lifecycle.New(store, lifecycle.Options{
		Now:   func() time.Time { return time.Date(2026, time.January, 1, 0, 1, 0, 0, time.UTC) },
		NewID: func() string { return preparationID },
	})
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.BeginPreparation(context.Background(), workspacePath, "agent/test"); err != nil {
		t.Fatal(err)
	}
	record := state.TrackedProcessRecord{PID: 42, StartedAt: "native:42", Command: []string{"installer"}}
	failed, err := service.MarkFailedRetainingProcess(context.Background(), workspacePath, preparationID, "installer cleanup unsafe", &record)
	if err != nil {
		t.Fatalf("MarkFailedRetainingProcess returned an error: %v", err)
	}
	if failed.Lifecycle != state.LifecycleFailed || len(failed.Processes) != 1 || failed.Processes[0].PID != 42 || failed.Processes[0].StartedAt != "native:42" {
		t.Fatalf("failed workspace = %#v, want exact retained process", failed)
	}
	if failed.UpdatedAt != "2026-01-01T00:01:00.001Z" {
		t.Fatalf("failed UpdatedAt = %q, want monotonic recovery fence", failed.UpdatedAt)
	}
}
