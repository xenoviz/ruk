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

const (
	assignmentID  = "13c9fd80-5f6b-42a7-9dd2-5ec3d9e67797"
	acquisitionID = "8f3f5ae3-30bf-4f5e-bbc6-53af31a48168"
)

func TestAssignmentHandoffIsOperationFenced(t *testing.T) {
	t.Parallel()

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
	preparing, err := service.BeginPreparation(context.Background(), workspacePath, "agent/old")
	if err != nil {
		t.Fatalf("BeginPreparation returned an error: %v", err)
	}

	assigned, err := service.MarkAssigned(context.Background(), workspacePath, *preparing.OperationID, lifecycle.AssignmentInput{
		Owner:     "agent",
		Hostname:  "host",
		Branch:    "agent/new",
		ExpiresAt: now.Add(8 * time.Hour),
	})
	if err != nil {
		t.Fatalf("MarkAssigned returned an error: %v", err)
	}
	if assigned.Lifecycle != state.LifecycleAssigned || assigned.OperationID == nil || *assigned.OperationID != acquisitionID {
		t.Fatalf("assigned workspace = %#v", assigned)
	}
	if assigned.Branch != "agent/new" || assigned.AvailableAt != nil || assigned.Failure != nil {
		t.Fatalf("assigned workspace metadata = %#v", assigned)
	}
	assignment := assigned.Assignment
	if assignment == nil || assignment.ID != assignmentID {
		t.Fatalf("Assignment = %#v", assignment)
	}
	if assignment.LeaseDurationMinutes != 480 || assignment.LastActivityAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("assignment lease = %#v", assignment)
	}
	if assignment.Ports == nil || assignment.LeaseKeepers == nil {
		t.Fatalf("assignment collections are nil: %#v", assignment)
	}

	_, err = service.RecordAcquisitionSuccess(context.Background(), assignmentID, preparationID, true)
	if err == nil || !strings.Contains(err.Error(), "Acquisition operation does not match") {
		t.Fatalf("RecordAcquisitionSuccess mismatch error = %v", err)
	}
	if store.current.Metrics.Acquisitions != 0 || store.current.Metrics.WorkspaceReuses != 0 {
		t.Fatalf("mismatched handoff changed metrics: %#v", store.current.Metrics)
	}

	now = now.Add(time.Minute)
	completed, err := service.RecordAcquisitionSuccess(context.Background(), assignmentID, acquisitionID, true)
	if err != nil {
		t.Fatalf("RecordAcquisitionSuccess returned an error: %v", err)
	}
	if completed.OperationID != nil || completed.UpdatedAt != "2026-01-01T00:01:00.000Z" {
		t.Fatalf("completed acquisition = %#v", completed)
	}
	if store.current.Metrics.Acquisitions != 1 || store.current.Metrics.WorkspaceReuses != 1 {
		t.Fatalf("completed metrics = %#v", store.current.Metrics)
	}
}
