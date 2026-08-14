package lifecycle_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
)

const keeperID = "093f6f14-9052-4edf-91c4-558ab76e97d8"

func TestActivityKeepersNeverShortenNewerRenewals(t *testing.T) {
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

	now = now.Add(time.Hour)
	active, err := service.BeginAssignmentActivity(context.Background(), assignmentID, keeperID, 30*time.Minute)
	if err != nil {
		t.Fatalf("BeginAssignmentActivity returned an error: %v", err)
	}
	if active.Assignment == nil || len(active.Assignment.LeaseKeepers) != 1 {
		t.Fatalf("active assignment = %#v", active.Assignment)
	}
	keeper := active.Assignment.LeaseKeepers[0]
	if keeper.ID != keeperID || keeper.HeartbeatAt != "2026-01-01T01:00:00.000Z" || keeper.ValidUntil != "2026-01-01T01:30:00.000Z" {
		t.Fatalf("keeper = %#v", keeper)
	}
	if active.Assignment.ExpiresAt != "2026-01-01T09:00:00.000Z" {
		t.Fatalf("activity expiry = %q, want stored 8-hour lease", active.Assignment.ExpiresAt)
	}

	now = time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	if _, err := service.RenewAssignment(context.Background(), assignmentID, now.Add(12*time.Hour), nil); err != nil {
		t.Fatalf("RenewAssignment returned an error: %v", err)
	}

	now = time.Date(2026, time.January, 1, 1, 30, 0, 0, time.UTC)
	refreshed, err := service.RefreshAssignmentActivity(context.Background(), assignmentID, keeperID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("RefreshAssignmentActivity returned an error: %v", err)
	}
	keeper = refreshed.Assignment.LeaseKeepers[0]
	if keeper.HeartbeatAt != "2026-01-01T02:00:00.000Z" || keeper.ValidUntil != "2026-01-01T03:00:00.000Z" {
		t.Fatalf("refreshed keeper = %#v", keeper)
	}
	if refreshed.Assignment.ExpiresAt != "2026-01-01T14:00:00.000Z" {
		t.Fatalf("refresh shortened explicit renewal to %q", refreshed.Assignment.ExpiresAt)
	}

	now = time.Date(2026, time.January, 1, 1, 45, 0, 0, time.UTC)
	finished, err := service.FinishAssignmentActivity(context.Background(), assignmentID, keeperID)
	if err != nil {
		t.Fatalf("FinishAssignmentActivity returned an error: %v", err)
	}
	if len(finished.Assignment.LeaseKeepers) != 0 {
		t.Fatalf("finished keepers = %#v", finished.Assignment.LeaseKeepers)
	}
	if finished.Assignment.RenewedAt != "2026-01-01T02:00:00.000Z" || finished.Assignment.ExpiresAt != "2026-01-01T14:00:00.000Z" {
		t.Fatalf("finish changed newer renewal: %#v", finished.Assignment)
	}
}
