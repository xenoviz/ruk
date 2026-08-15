package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
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

// ReserveAvailableWorkspace atomically reserves the oldest available workspace.
// When requestedPath is supplied, only that workspace can be selected. A nil
// result means that no eligible workspace exists; assignment publication leaves
// an opaque operation marker until acquisition completes.
func (service *Service) ReserveAvailableWorkspace(ctx context.Context, input AssignmentInput, requestedPath ...string) (*state.WorkspaceRecord, error) {
	if len(requestedPath) > 1 {
		return nil, errors.New("requested workspace path may be supplied at most once")
	}
	if err := validateAssignmentInput(input); err != nil {
		return nil, err
	}

	var requestedKey string
	if len(requestedPath) == 1 {
		resolved, err := filepath.Abs(filepath.Clean(requestedPath[0]))
		if err != nil {
			return nil, fmt.Errorf("resolve workspace path: %w", err)
		}
		requestedKey, err = state.TreeKey(resolved)
		if err != nil {
			return nil, err
		}
	}

	var result *state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		candidates := make([]struct {
			key       string
			workspace state.WorkspaceRecord
		}, 0)
		for key, workspace := range current.Workspaces {
			if workspace.Lifecycle != state.LifecycleAvailable || workspace.OperationID != nil {
				continue
			}
			if requestedKey != "" && key != requestedKey {
				continue
			}
			candidates = append(candidates, struct {
				key       string
				workspace state.WorkspaceRecord
			}{key: key, workspace: workspace})
		}
		sort.Slice(candidates, func(left, right int) bool {
			leftAvailable := ""
			if candidates[left].workspace.AvailableAt != nil {
				leftAvailable = *candidates[left].workspace.AvailableAt
			}
			rightAvailable := ""
			if candidates[right].workspace.AvailableAt != nil {
				rightAvailable = *candidates[right].workspace.AvailableAt
			}
			return leftAvailable < rightAvailable || (leftAvailable == rightAvailable && candidates[left].workspace.Path < candidates[right].workspace.Path)
		})
		if len(candidates) == 0 {
			return nil
		}

		key := candidates[0].key
		workspace := candidates[0].workspace
		assigned, err := service.assign(workspace, input)
		if err != nil {
			return err
		}
		current.Workspaces[key] = assigned
		copy := assigned
		result = &copy
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// MarkAssigned publishes ownership of a prepared workspace while retaining an acquisition fence.
func (service *Service) MarkAssigned(ctx context.Context, workspacePath, preparationOperationID string, input AssignmentInput) (state.WorkspaceRecord, error) {
	if err := validateAssignmentInput(input); err != nil {
		return state.WorkspaceRecord{}, err
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

		assigned, err := service.assign(workspace, input)
		if err != nil {
			return err
		}
		current.Workspaces[key] = assigned
		result = assigned
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

// RetainAssignmentAfterAcquisitionFailure clears an acquisition marker while
// preserving the exact assignment for a later, ID-fenced recovery or release.
func (service *Service) RetainAssignmentAfterAcquisitionFailure(ctx context.Context, assignmentID, acquisitionOperationID, failure string) (state.WorkspaceRecord, error) {
	if failure == "" {
		return state.WorkspaceRecord{}, errors.New("failure must not be empty")
	}
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
		now := timestamp(service.now())
		workspace.OperationID = nil
		workspace.UpdatedAt = now
		workspace.Failure = &failure
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

func validateAssignmentInput(input AssignmentInput) error {
	if input.Owner == "" {
		return errors.New("owner must not be empty")
	}
	if input.Hostname == "" {
		return errors.New("hostname must not be empty")
	}
	return nil
}

func (service *Service) assign(workspace state.WorkspaceRecord, input AssignmentInput) (state.WorkspaceRecord, error) {
	nowValue := service.now().UTC().Truncate(time.Millisecond)
	expiresValue := input.ExpiresAt.UTC().Truncate(time.Millisecond)
	if !expiresValue.After(nowValue) {
		return state.WorkspaceRecord{}, errors.New("expiresAt must be after now")
	}
	now := timestamp(nowValue)
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
		ExpiresAt:            timestamp(expiresValue),
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
	return workspace, nil
}
