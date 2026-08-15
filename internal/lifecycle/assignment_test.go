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

func TestReserveAvailableWorkspaceSelectsAndPublishesAtomically(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		requestedPath string
		available     []struct {
			path        string
			availableAt string
			operation   *string
			lifecycle   state.WorkspaceLifecycle
		}
		wantPath string
		wantNil  bool
	}{
		{
			name: "oldest then path tie break",
			available: []struct {
				path        string
				availableAt string
				operation   *string
				lifecycle   state.WorkspaceLifecycle
			}{
				{path: "z-old", availableAt: "2026-01-01T01:00:00.000Z", lifecycle: state.LifecycleAvailable},
				{path: "b-tie", availableAt: "2026-01-01T00:00:00.000Z", lifecycle: state.LifecycleAvailable},
				{path: "a-tie", availableAt: "2026-01-01T00:00:00.000Z", lifecycle: state.LifecycleAvailable},
				{path: "reserved", availableAt: "2026-01-01T00:00:00.000Z", operation: stringPointer(acquisitionID), lifecycle: state.LifecycleAvailable},
				{path: "assigned", availableAt: "2026-01-01T00:00:00.000Z", lifecycle: state.LifecycleAssigned},
			},
			wantPath: "a-tie",
		},
		{
			name:          "requested path narrows selection",
			requestedPath: "z-old",
			available: []struct {
				path        string
				availableAt string
				operation   *string
				lifecycle   state.WorkspaceLifecycle
			}{
				{path: "z-old", availableAt: "2026-01-01T01:00:00.000Z", lifecycle: state.LifecycleAvailable},
				{path: "a-newer", availableAt: "2026-01-01T02:00:00.000Z", lifecycle: state.LifecycleAvailable},
			},
			wantPath: "z-old",
		},
		{
			name: "no operation free capacity",
			available: []struct {
				path        string
				availableAt string
				operation   *string
				lifecycle   state.WorkspaceLifecycle
			}{
				{path: "reserved", availableAt: "2026-01-01T00:00:00.000Z", operation: stringPointer(acquisitionID), lifecycle: state.LifecycleAvailable},
				{path: "preparing", availableAt: "2026-01-01T00:00:00.000Z", lifecycle: state.LifecyclePreparing},
			},
			wantNil: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()
			workspaceRoot := t.TempDir()
			workspacePaths := make(map[string]string, len(testCase.available))
			for _, entry := range testCase.available {
				workspacePaths[entry.path] = addAvailableWorkspace(t, store, filepath.Join(workspaceRoot, entry.path), entry.availableAt, entry.operation, entry.lifecycle)
			}
			identifiers := []string{assignmentID, acquisitionID}
			now := time.Date(2026, time.January, 1, 2, 0, 0, 0, time.UTC)
			service := lifecycle.New(store, lifecycle.Options{
				Now: func() time.Time { return now },
				NewID: func() string {
					identifier := identifiers[0]
					identifiers = identifiers[1:]
					return identifier
				},
			})

			var reserved *state.WorkspaceRecord
			var err error
			if testCase.requestedPath == "" {
				reserved, err = service.ReserveAvailableWorkspace(context.Background(), lifecycle.AssignmentInput{
					Owner: "agent", Hostname: "host", ExpiresAt: now.Add(8 * time.Hour),
				})
			} else {
				reserved, err = service.ReserveAvailableWorkspace(context.Background(), lifecycle.AssignmentInput{
					Owner: "agent", Hostname: "host", ExpiresAt: now.Add(8 * time.Hour),
				}, workspacePaths[testCase.requestedPath])
			}
			if err != nil {
				t.Fatalf("ReserveAvailableWorkspace returned an error: %v", err)
			}
			if testCase.wantNil {
				if reserved != nil {
					t.Fatalf("reservation = %#v, want nil", reserved)
				}
				if len(identifiers) != 2 {
					t.Fatalf("no-capacity reservation consumed IDs: %v", identifiers)
				}
				return
			}
			if reserved == nil || filepath.Base(reserved.Path) != testCase.wantPath {
				t.Fatalf("reservation = %#v, want path %q", reserved, testCase.wantPath)
			}
			if reserved.Lifecycle != state.LifecycleAssigned || reserved.Assignment == nil || reserved.OperationID == nil {
				t.Fatalf("reservation did not publish assignment handoff: %#v", reserved)
			}
			if *reserved.OperationID != acquisitionID || reserved.Assignment.ID != assignmentID {
				t.Fatalf("reservation fences = assignment %q operation %q", reserved.Assignment.ID, *reserved.OperationID)
			}
			if reserved.AvailableAt != nil || reserved.Failure != nil {
				t.Fatalf("reservation retained pool metadata: %#v", reserved)
			}
		})
	}
}

func TestAcquisitionHandoffFencesAndRecoveryPreserveRenewal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		action func(*lifecycle.Service, string, string) error
	}{
		{
			name: "wrong operation",
			action: func(service *lifecycle.Service, assignment, operation string) error {
				_, err := service.RecordAcquisitionSuccess(context.Background(), assignment, preparationID, false)
				return err
			},
		},
		{
			name: "wrong assignment",
			action: func(service *lifecycle.Service, _, operation string) error {
				_, err := service.RecordAcquisitionSuccess(context.Background(), preparationID, operation, false)
				return err
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryStore()
			workspacePath := addAvailableWorkspace(t, store, testCase.name, "2026-01-01T00:00:00.000Z", nil, state.LifecycleAvailable)
			now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			identifiers := []string{assignmentID, acquisitionID}
			service := lifecycle.New(store, lifecycle.Options{
				Now: func() time.Time { return now },
				NewID: func() string {
					identifier := identifiers[0]
					identifiers = identifiers[1:]
					return identifier
				},
			})
			reserved, err := service.ReserveAvailableWorkspace(context.Background(), lifecycle.AssignmentInput{
				Owner: "agent", Hostname: "host", ExpiresAt: now.Add(8 * time.Hour),
			})
			if err != nil || reserved == nil || reserved.Assignment == nil || reserved.OperationID == nil {
				t.Fatalf("reservation = %#v, error = %v", reserved, err)
			}
			if err := testCase.action(service, reserved.Assignment.ID, *reserved.OperationID); err == nil {
				t.Fatal("mismatched handoff unexpectedly succeeded")
			}
			key, _ := state.TreeKey(workspacePath)
			if store.current.Workspaces[key].OperationID == nil || *store.current.Workspaces[key].OperationID != acquisitionID {
				t.Fatalf("mismatched handoff changed operation marker: %#v", store.current.Workspaces[key])
			}
		})
	}

	store := newMemoryStore()
	addAvailableWorkspace(t, store, "renewed", "2026-01-01T00:00:00.000Z", nil, state.LifecycleAvailable)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	identifiers := []string{assignmentID, acquisitionID}
	service := lifecycle.New(store, lifecycle.Options{
		Now: func() time.Time { return now },
		NewID: func() string {
			identifier := identifiers[0]
			identifiers = identifiers[1:]
			return identifier
		},
	})
	reserved, err := service.ReserveAvailableWorkspace(context.Background(), lifecycle.AssignmentInput{
		Owner: "agent", Hostname: "host", ExpiresAt: now.Add(8 * time.Hour),
	})
	if err != nil || reserved == nil || reserved.Assignment == nil || reserved.OperationID == nil {
		t.Fatalf("reservation = %#v, error = %v", reserved, err)
	}
	now = now.Add(time.Hour)
	renewed, err := service.RenewAssignment(context.Background(), assignmentID, now.Add(4*time.Hour), nil)
	if err != nil {
		t.Fatalf("RenewAssignment returned an error: %v", err)
	}
	if renewed.Assignment == nil || renewed.Assignment.ExpiresAt != "2026-01-01T05:00:00.000Z" {
		t.Fatalf("renewed assignment = %#v", renewed.Assignment)
	}
	if _, err := service.BeginWorkspaceReturn(context.Background(), assignmentID); err == nil || !strings.Contains(err.Error(), "acquisition is still in progress") {
		t.Fatalf("ordinary return crossed acquisition fence: %v", err)
	}

	now = now.Add(time.Minute)
	retained, err := service.RetainAssignmentAfterAcquisitionFailure(context.Background(), assignmentID, acquisitionID, "installer failed")
	if err != nil {
		t.Fatalf("RetainAssignmentAfterAcquisitionFailure returned an error: %v", err)
	}
	if retained.OperationID != nil || retained.Failure == nil || *retained.Failure != "installer failed" {
		t.Fatalf("retained assignment = %#v", retained)
	}
	if retained.Assignment == nil || retained.Assignment.ExpiresAt != "2026-01-01T05:00:00.000Z" {
		t.Fatalf("retained renewal was overwritten: %#v", retained.Assignment)
	}
	if _, err := service.BeginWorkspaceReturn(context.Background(), assignmentID); err != nil {
		t.Fatalf("retained assignment was not retryable: %v", err)
	}
}

func addAvailableWorkspace(t *testing.T, store *memoryStore, name, availableAt string, operationID *string, lifecycleState state.WorkspaceLifecycle) string {
	t.Helper()
	workspacePath := name
	if !filepath.IsAbs(workspacePath) {
		workspacePath = filepath.Join(t.TempDir(), workspacePath)
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	available := availableAt
	store.current.Workspaces[key] = state.WorkspaceRecord{
		Path: workspacePath, Managed: true, Branch: "agent/test", Lifecycle: lifecycleState,
		OperationID: operationID, Assignment: nil, Processes: []state.TrackedProcessRecord{},
		CreatedAt: availableAt, UpdatedAt: availableAt, AvailableAt: &available,
	}
	return workspacePath
}
