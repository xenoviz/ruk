package lifecycle_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
)

func TestRenewalUsesExpectedTimestampFence(t *testing.T) {
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
	staleFence := "2025-12-31T23:59:00.000Z"
	unchanged, err := service.RenewAssignment(context.Background(), assignmentID, now.Add(12*time.Hour), &staleFence)
	if err != nil {
		t.Fatalf("stale RenewAssignment returned an error: %v", err)
	}
	if unchanged.Assignment == nil || unchanged.Assignment.RenewedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("stale renewal changed assignment: %#v", unchanged.Assignment)
	}

	expected := "2026-01-01T00:00:00.000Z"
	renewed, err := service.RenewAssignment(context.Background(), assignmentID, now.Add(4*time.Hour), &expected)
	if err != nil {
		t.Fatalf("RenewAssignment returned an error: %v", err)
	}
	if renewed.Assignment == nil {
		t.Fatal("renewed assignment is nil")
	}
	if renewed.Assignment.ID != assignmentID || renewed.Assignment.RenewedAt != "2026-01-01T01:00:00.000Z" {
		t.Fatalf("renewed assignment identity = %#v", renewed.Assignment)
	}
	if renewed.Assignment.ExpiresAt != "2026-01-01T05:00:00.000Z" || renewed.Assignment.LeaseDurationMinutes != 240 {
		t.Fatalf("renewed lease = %#v", renewed.Assignment)
	}
	if renewed.Assignment.LastActivityAt != renewed.Assignment.RenewedAt || renewed.UpdatedAt != renewed.Assignment.RenewedAt {
		t.Fatalf("renewal activity timestamps are inconsistent: %#v", renewed)
	}
}
