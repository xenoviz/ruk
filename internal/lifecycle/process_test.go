package lifecycle_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

func TestAssignmentProcessRegistrationIsIdentityFenced(t *testing.T) {
	t.Parallel()

	_, service, _, now := assignedService(t)
	record := state.TrackedProcessRecord{
		PID:       42,
		GroupID:   int64Pointer(42),
		Command:   []string{"git", "status"},
		StartedAt: "process-start",
	}

	*now = now.Add(time.Minute)
	registered, err := service.AddAssignmentProcess(context.Background(), assignmentID, record)
	if err != nil {
		t.Fatalf("AddAssignmentProcess returned an error: %v", err)
	}
	if len(registered.Processes) != 1 || registered.Processes[0].StartedAt != record.StartedAt {
		t.Fatalf("registered processes = %#v", registered.Processes)
	}
	record.Command[0] = "changed"
	if registered.Processes[0].Command[0] != "git" {
		t.Fatal("registered command aliases caller memory")
	}

	_, err = service.AddAssignmentProcess(context.Background(), assignmentID, state.TrackedProcessRecord{
		PID:       42,
		StartedAt: "replacement",
	})
	if err == nil || !strings.Contains(err.Error(), "already tracked") {
		t.Fatalf("duplicate process error = %v", err)
	}

	_, err = service.RemoveAssignmentProcess(context.Background(), assignmentID, 42, "replacement")
	if err == nil || !strings.Contains(err.Error(), "is not tracked") {
		t.Fatalf("identity mismatch error = %v", err)
	}

	*now = now.Add(time.Minute)
	removed, err := service.RemoveAssignmentProcess(context.Background(), assignmentID, 42, "process-start")
	if err != nil {
		t.Fatalf("RemoveAssignmentProcess returned an error: %v", err)
	}
	if len(removed.Processes) != 0 {
		t.Fatalf("remaining processes = %#v", removed.Processes)
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}
