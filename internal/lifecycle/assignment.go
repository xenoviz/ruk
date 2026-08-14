package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

// AssignmentInput describes a new immutable assignment and its initial lease.
type AssignmentInput struct {
	Owner     string
	Hostname  string
	Branch    string
	ExpiresAt time.Time
}

// MarkAssigned publishes ownership of a prepared workspace while retaining an acquisition fence.
func (service *Service) MarkAssigned(ctx context.Context, workspacePath, preparationOperationID string, input AssignmentInput) (state.WorkspaceRecord, error) {
	if input.Owner == "" {
		return state.WorkspaceRecord{}, errors.New("owner must not be empty")
	}
	if input.Hostname == "" {
		return state.WorkspaceRecord{}, errors.New("hostname must not be empty")
	}
	resolved, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return state.WorkspaceRecord{}, fmt.Errorf("resolve workspace path: %w", err)
	}
	key, err := state.TreeKey(resolved)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}

	var result state.WorkspaceRecord
	err = service.store.Update(ctx, func(current *state.State) error {
		workspace, exists := current.Workspaces[key]
		if !exists {
			return fmt.Errorf("Workspace %s is not managed", resolved)
		}
		if workspace.Lifecycle != state.LifecyclePreparing {
			return fmt.Errorf("Workspace %s is %s, expected preparing", workspace.Path, workspace.Lifecycle)
		}
		if workspace.OperationID == nil || *workspace.OperationID != preparationOperationID {
			return errors.New("Preparation operation does not match")
		}

		nowValue := service.now().UTC().Truncate(time.Millisecond)
		expiresValue := input.ExpiresAt.UTC().Truncate(time.Millisecond)
		if !expiresValue.After(nowValue) {
			return errors.New("expiresAt must be after now")
		}
		now := timestamp(nowValue)
		expiresAt := timestamp(expiresValue)
		assignmentID := service.newID()
		acquisitionOperationID := service.newID()
		workspace.Lifecycle = state.LifecycleAssigned
		workspace.OperationID = &acquisitionOperationID
		workspace.Assignment = &state.AssignmentRecord{
			ID:                   assignmentID,
			Owner:                input.Owner,
			Hostname:             input.Hostname,
			AssignedAt:           now,
			RenewedAt:            now,
			ExpiresAt:            expiresAt,
			LeaseDurationMinutes: expiresValue.Sub(nowValue).Minutes(),
			LastActivityAt:       now,
			LeaseKeepers:         []state.LeaseKeeperRecord{},
			Ports:                map[string]int64{},
		}
		if input.Branch != "" {
			workspace.Branch = input.Branch
		}
		workspace.UpdatedAt = now
		workspace.AvailableAt = nil
		workspace.Failure = nil
		current.Workspaces[key] = workspace
		result = workspace
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

// RecordAcquisitionSuccess clears the acquisition fence after preparation and port allocation finish.
func (service *Service) RecordAcquisitionSuccess(ctx context.Context, assignmentID, acquisitionOperationID string, reused bool) (state.WorkspaceRecord, error) {
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, exists := findAssignment(current, assignmentID)
		if !exists {
			return fmt.Errorf("Assignment %s does not exist", assignmentID)
		}
		if workspace.Lifecycle != state.LifecycleAssigned {
			return fmt.Errorf("Workspace %s is %s, expected assigned", workspace.Path, workspace.Lifecycle)
		}
		if workspace.OperationID == nil || *workspace.OperationID != acquisitionOperationID {
			return errors.New("Acquisition operation does not match")
		}
		workspace.OperationID = nil
		workspace.UpdatedAt = timestamp(service.now())
		current.Workspaces[key] = workspace
		current.Metrics.Acquisitions++
		if reused {
			current.Metrics.WorkspaceReuses++
		}
		result = workspace
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

func findAssignment(current *state.State, assignmentID string) (string, state.WorkspaceRecord, bool) {
	for key, workspace := range current.Workspaces {
		if workspace.Assignment != nil && workspace.Assignment.ID == assignmentID {
			return key, workspace, true
		}
	}
	return "", state.WorkspaceRecord{}, false
}
