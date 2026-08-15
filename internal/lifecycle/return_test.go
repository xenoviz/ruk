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

func TestWorkspaceReturnRetainsOwnershipUntilProcessesFinish(t *testing.T) {
	t.Parallel()

	store, service, workspacePath, now := assignedService(t)
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	workspace := store.current.Workspaces[key]
	workspace.Processes = []state.TrackedProcessRecord{{PID: 123, StartedAt: "identity"}}
	store.current.Workspaces[key] = workspace

	*now = now.Add(time.Hour)
	returning, err := service.BeginWorkspaceReturn(context.Background(), assignmentID)
	if err != nil {
		t.Fatalf("BeginWorkspaceReturn returned an error: %v", err)
	}
	if returning.Lifecycle != state.LifecycleReturning || returning.Assignment == nil || returning.Assignment.ID != assignmentID {
		t.Fatalf("returning workspace = %#v", returning)
	}

	_, err = service.FinishWorkspaceReturn(context.Background(), assignmentID)
	if err == nil || !strings.Contains(err.Error(), "still has tracked processes") {
		t.Fatalf("FinishWorkspaceReturn process error = %v", err)
	}
	workspace = store.current.Workspaces[key]
	workspace.Processes = []state.TrackedProcessRecord{}
	store.current.Workspaces[key] = workspace

	*now = now.Add(time.Minute)
	available, err := service.FinishWorkspaceReturn(context.Background(), assignmentID)
	if err != nil {
		t.Fatalf("FinishWorkspaceReturn returned an error: %v", err)
	}
	if available.Lifecycle != state.LifecycleAvailable || available.Assignment != nil || available.OperationID != nil {
		t.Fatalf("available workspace = %#v", available)
	}
	if available.AvailableAt == nil || *available.AvailableAt != "2026-01-01T01:01:00.000Z" {
		t.Fatalf("AvailableAt = %#v", available.AvailableAt)
	}
}

func TestCancelledReturnRestoresAssignmentAndFailure(t *testing.T) {
	t.Parallel()

	_, service, _, now := assignedService(t)
	*now = now.Add(time.Hour)
	if _, err := service.BeginWorkspaceReturn(context.Background(), assignmentID); err != nil {
		t.Fatalf("BeginWorkspaceReturn returned an error: %v", err)
	}
	*now = now.Add(time.Minute)
	restored, err := service.CancelWorkspaceReturn(context.Background(), assignmentID, "cleanup failed")
	if err != nil {
		t.Fatalf("CancelWorkspaceReturn returned an error: %v", err)
	}
	if restored.Lifecycle != state.LifecycleAssigned || restored.Assignment == nil || restored.Assignment.ID != assignmentID {
		t.Fatalf("restored workspace = %#v", restored)
	}
	if restored.Failure == nil || *restored.Failure != "cleanup failed" {
		t.Fatalf("Failure = %#v", restored.Failure)
	}
}

func TestWorkspaceReturnFencesAreTableDriven(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		action func(*testing.T, *memoryStore, *lifecycle.Service, *time.Time) error
		want   string
	}{
		{
			name: "success",
			action: func(t *testing.T, _ *memoryStore, service *lifecycle.Service, now *time.Time) error {
				*now = now.Add(time.Hour)
				workspace, err := service.BeginWorkspaceReturn(context.Background(), assignmentID)
				if err == nil && workspace.Lifecycle != state.LifecycleReturning {
					t.Fatalf("workspace lifecycle = %q", workspace.Lifecycle)
				}
				return err
			},
		},
		{
			name: "returning retry is idempotent",
			action: func(t *testing.T, _ *memoryStore, service *lifecycle.Service, now *time.Time) error {
				*now = now.Add(time.Hour)
				first, err := service.BeginWorkspaceReturn(context.Background(), assignmentID)
				if err != nil {
					return err
				}
				*now = now.Add(time.Minute)
				second, err := service.BeginWorkspaceReturnWithOptions(context.Background(), assignmentID, lifecycle.ReturnOptions{
					RequireExpiredBy: "not-a-timestamp", AcquisitionOperationID: "wrong-op", ExpectedUpdatedAt: "stale-update",
				})
				if err == nil && second.UpdatedAt != first.UpdatedAt {
					t.Fatalf("idempotent retry changed timestamp from %q to %q", first.UpdatedAt, second.UpdatedAt)
				}
				return err
			},
		},
		{
			name: "concurrent renewal skips stale expiry snapshot",
			action: func(t *testing.T, _ *memoryStore, service *lifecycle.Service, now *time.Time) error {
				*now = now.Add(time.Hour)
				if _, err := service.RenewAssignment(context.Background(), assignmentID, now.Add(12*time.Hour), nil); err != nil {
					return err
				}
				workspace, err := service.BeginWorkspaceReturnWithOptions(context.Background(), assignmentID, lifecycle.ReturnOptions{RequireExpiredBy: "2026-01-01T08:00:00.000Z"})
				if err == nil {
					t.Fatalf("stale expiry unexpectedly entered return: %#v", workspace)
				}
				return err
			},
			want: "was renewed before collection",
		},
		{
			name: "concurrent renewal skips stale update snapshot",
			action: func(t *testing.T, _ *memoryStore, service *lifecycle.Service, now *time.Time) error {
				stale := "2026-01-01T00:00:00.000Z"
				*now = now.Add(time.Hour)
				if _, err := service.RenewAssignment(context.Background(), assignmentID, now.Add(4*time.Hour), nil); err != nil {
					return err
				}
				_, err := service.BeginWorkspaceReturnWithOptions(context.Background(), assignmentID, lifecycle.ReturnOptions{ExpectedUpdatedAt: stale})
				return err
			},
			want: "changed before collection",
		},
		{
			name: "acquisition handoff is fenced and restored on cancellation",
			action: func(t *testing.T, store *memoryStore, service *lifecycle.Service, now *time.Time) error {
				key, err := state.TreeKey(filepath.Join(t.TempDir(), "unused"))
				if err != nil {
					return err
				}
				// Use the only managed record rather than relying on its temporary path.
				for candidateKey := range store.current.Workspaces {
					key = candidateKey
					break
				}
				operation := acquisitionID
				workspace := store.current.Workspaces[key]
				workspace.OperationID = &operation
				store.current.Workspaces[key] = workspace
				if _, err := service.BeginWorkspaceReturn(context.Background(), assignmentID); err == nil {
					t.Fatal("ordinary release crossed acquisition marker")
				}
				if _, err := service.BeginWorkspaceReturn(context.Background(), assignmentID, acquisitionID); err != nil {
					return err
				}
				*now = now.Add(time.Minute)
				cancelled, err := service.CancelWorkspaceReturn(context.Background(), assignmentID, "handoff cleanup failed")
				if err != nil {
					return err
				}
				if cancelled.OperationID == nil || *cancelled.OperationID != acquisitionID {
					return errors.New("cancelled recovery lost acquisition marker")
				}
				return nil
			},
		},
		{
			name: "stale acquisition operation is rejected",
			action: func(t *testing.T, _ *memoryStore, service *lifecycle.Service, _ *time.Time) error {
				_, err := service.BeginWorkspaceReturnWithOptions(context.Background(), assignmentID, lifecycle.ReturnOptions{AcquisitionOperationID: "stale-operation"})
				return err
			},
			want: "Assignment " + assignmentID + " acquisition operation does not match",
		},
		{
			name: "empty cancellation reason is machine-readable",
			action: func(t *testing.T, _ *memoryStore, service *lifecycle.Service, now *time.Time) error {
				*now = now.Add(time.Hour)
				if _, err := service.BeginWorkspaceReturn(context.Background(), assignmentID); err != nil {
					return err
				}
				_, err := service.CancelWorkspaceReturn(context.Background(), assignmentID, "")
				return err
			},
			want: "failure must not be empty",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store, service, _, now := assignedService(t)
			err := testCase.action(t, store, service, now)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("action returned an error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want machine-readable text containing %q", err, testCase.want)
			}
		})
	}
}

func assignedService(t *testing.T) (*memoryStore, *lifecycle.Service, string, *time.Time) {
	t.Helper()
	store := newMemoryStore()
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	identifiers := []string{preparationID, assignmentID, acquisitionID}
	service := lifecycle.New(store, lifecycle.Options{
		Now: func() time.Time { return now },
		NewID: func() string {
			identifier := identifiers[0]
			identifiers = identifiers[1:]
			return identifier
		},
	})
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	preparing, err := service.BeginPreparation(context.Background(), workspacePath, "agent/test")
	if err != nil {
		t.Fatalf("BeginPreparation returned an error: %v", err)
	}
	assigned, err := service.MarkAssigned(context.Background(), workspacePath, *preparing.OperationID, lifecycle.AssignmentInput{
		Owner:     "agent",
		Hostname:  "host",
		ExpiresAt: now.Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatalf("MarkAssigned returned an error: %v", err)
	}
	if _, err := service.RecordAcquisitionSuccess(context.Background(), assignmentID, *assigned.OperationID, false); err != nil {
		t.Fatalf("RecordAcquisitionSuccess returned an error: %v", err)
	}
	return store, service, workspacePath, &now
}
