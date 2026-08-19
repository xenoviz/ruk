package lifecycle

import (
	"errors"
	"testing"

	"github.com/xenoviz/ruk/internal/state"
)

func TestAcquisitionReleaseFailurePreservesCommittedAssignmentMetadata(t *testing.T) {
	releaseErr := errors.New("workspace lock release failed")
	result := AcquisitionResult{
		AssignmentID: "assignment-1",
		Path:         "/pool/slot-a",
		Workspace: state.WorkspaceRecord{
			Path:       "/pool/slot-a",
			Assignment: &state.AssignmentRecord{ID: "assignment-1", ExpiresAt: "2026-01-01T08:00:00.000Z"},
		},
	}
	err := acquisitionReleaseFailure(result, nil, releaseErr)
	var retained *RetainedAssignmentError
	if !errors.As(err, &retained) {
		t.Fatalf("error = %T, want retained assignment metadata", err)
	}
	if retained.AssignmentID != "assignment-1" || retained.Path != "/pool/slot-a" || retained.ExpiresAt != "2026-01-01T08:00:00.000Z" || retained.Recovery != "ruk release assignment-1" {
		t.Fatalf("retained metadata = %#v", retained)
	}
	if !errors.Is(err, releaseErr) {
		t.Fatalf("error does not preserve release cause: %v", err)
	}
}
