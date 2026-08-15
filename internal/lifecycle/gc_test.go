package lifecycle_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

func TestIdentifyGCCandidatesPreservesFencesAndSortsDryRun(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	store.current.Workspaces = map[string]state.WorkspaceRecord{
		"available":  gcWorkspace("/pool/available", state.LifecycleAvailable, nil, nil, "2026-01-01T01:00:00.000Z"),
		"failed":     gcWorkspace("/pool/failed", state.LifecycleFailed, nil, nil, "2026-01-01T01:00:00.000Z"),
		"preparing":  gcWorkspace("/pool/preparing", state.LifecyclePreparing, stringPointer("prepare-op"), nil, "2026-01-01T00:00:00.000Z"),
		"acquiring":  gcWorkspace("/pool/acquiring", state.LifecycleAssigned, stringPointer("acquire-op"), gcAssignment("acquire-id", "2026-01-01T03:00:00.000Z", nil), "2026-01-01T00:00:00.000Z"),
		"collecting": gcWorkspace("/pool/collecting", state.LifecycleAvailable, stringPointer("collect-op"), nil, "2026-01-01T00:00:00.000Z"),
		"expired":    gcWorkspace("/pool/expired", state.LifecycleAssigned, nil, gcAssignment("expired-id", "2026-01-01T01:00:00.000Z", nil), "2026-01-01T00:00:00.000Z"),
		"active":     gcWorkspace("/pool/active", state.LifecycleAssigned, nil, gcAssignment("active-id", "2026-01-01T01:00:00.000Z", []state.LeaseKeeperRecord{{ID: "keeper", ValidUntil: "2026-01-01T03:00:00.000Z"}}), "2026-01-01T00:00:00.000Z"),
	}
	before := cloneStateForGC(store.current)

	candidates, err := lifecycle.IdentifyGCCandidates(store.current, time.Date(2026, time.January, 1, 1, 0, 0, 0, time.UTC), now, true)
	if err != nil {
		t.Fatalf("IdentifyGCCandidates returned an error: %v", err)
	}
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.Workspace.Path+":"+string(candidate.Reason))
	}
	want := []string{
		"/pool/acquiring:abandoned-acquisition",
		"/pool/available:available",
		"/pool/collecting:interrupted-collection",
		"/pool/expired:expired-assignment",
		"/pool/failed:failed",
		"/pool/preparing:abandoned-preparation",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for _, candidate := range candidates {
		if candidate.ExpectedUpdatedAt != candidate.Workspace.UpdatedAt {
			t.Fatalf("candidate update fence = %#v", candidate)
		}
		if candidate.Workspace.OperationID != nil && (candidate.ExpectedOperationID == nil || *candidate.ExpectedOperationID != *candidate.Workspace.OperationID) {
			t.Fatalf("candidate operation fence = %#v", candidate)
		}
		if candidate.Workspace.Assignment != nil && (candidate.ExpectedAssignmentID == nil || *candidate.ExpectedAssignmentID != candidate.Workspace.Assignment.ID) {
			t.Fatalf("candidate assignment fence = %#v", candidate)
		}
	}
	if !reflect.DeepEqual(store.current, before) {
		t.Fatal("candidate identification mutated state")
	}
}

func TestIdentifyGCCandidatesDefaultsToSafeRecordsAndDoesNotMutate(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
	store.current.Workspaces = map[string]state.WorkspaceRecord{
		"available": gcWorkspace("/pool/available", state.LifecycleAvailable, nil, nil, "2026-01-01T01:00:00.000Z"),
		"preparing": gcWorkspace("/pool/preparing", state.LifecyclePreparing, stringPointer("prepare-op"), nil, "2026-01-01T00:00:00.000Z"),
		"expired":   gcWorkspace("/pool/expired", state.LifecycleAssigned, nil, gcAssignment("expired-id", "2026-01-01T01:00:00.000Z", nil), "2026-01-01T00:00:00.000Z"),
	}
	before := cloneStateForGC(store.current)

	candidates, err := lifecycle.IdentifyGcCandidates(store.current, time.Date(2026, time.January, 1, 1, 0, 0, 0, time.UTC), now, false)
	if err != nil {
		t.Fatalf("IdentifyGcCandidates returned an error: %v", err)
	}
	if len(candidates) != 2 || candidates[0].Reason != lifecycle.GcAvailable || candidates[1].Reason != lifecycle.GcExpiredAssignment {
		t.Fatalf("safe dry-run candidates = %#v", candidates)
	}
	if !reflect.DeepEqual(store.current, before) {
		t.Fatal("candidate identification mutated state")
	}
}

func gcWorkspace(path string, lifecycleState state.WorkspaceLifecycle, operationID *string, assignment *state.AssignmentRecord, updatedAt string) state.WorkspaceRecord {
	availableAt := updatedAt
	return state.WorkspaceRecord{
		Path: path, Managed: true, Branch: "agent/test", Lifecycle: lifecycleState,
		OperationID: operationID, Assignment: assignment, Processes: []state.TrackedProcessRecord{},
		CreatedAt: updatedAt, UpdatedAt: updatedAt, AvailableAt: &availableAt,
	}
}

func gcAssignment(id, expiresAt string, keepers []state.LeaseKeeperRecord) *state.AssignmentRecord {
	return &state.AssignmentRecord{
		ID: id, Owner: "agent", Hostname: "host", AssignedAt: "2026-01-01T00:00:00.000Z",
		RenewedAt: "2026-01-01T00:00:00.000Z", ExpiresAt: expiresAt, LeaseDurationMinutes: 60,
		LastActivityAt: "2026-01-01T00:00:00.000Z", LeaseKeepers: keepers, Ports: map[string]int64{},
	}
}

func stringPointer(value string) *string { return &value }

func cloneStateForGC(current *state.State) *state.State {
	clone := *current
	clone.Workspaces = make(map[string]state.WorkspaceRecord, len(current.Workspaces))
	for key, workspace := range current.Workspaces {
		clone.Workspaces[key] = workspace
	}
	return &clone
}
