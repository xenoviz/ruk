package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/xenoviz/ruk/internal/state"
)

// BeginWorkspaceReturn fences an assignment while cleanup runs.
func (service *Service) BeginWorkspaceReturn(ctx context.Context, assignmentID string) (state.WorkspaceRecord, error) {
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, err := assignedWorkspace(current, assignmentID)
		if err != nil {
			return err
		}
		workspace.Lifecycle = state.LifecycleReturning
		workspace.UpdatedAt = timestamp(service.now())
		current.Workspaces[key] = workspace
		result = workspace
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

// FinishWorkspaceReturn publishes a cleaned assignment as reusable capacity.
func (service *Service) FinishWorkspaceReturn(ctx context.Context, assignmentID string) (state.WorkspaceRecord, error) {
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, exists := findAssignment(current, assignmentID)
		if !exists {
			return fmt.Errorf("Assignment %s does not exist", assignmentID)
		}
		if workspace.Lifecycle != state.LifecycleReturning {
			return fmt.Errorf("Workspace %s is %s, expected returning", workspace.Path, workspace.Lifecycle)
		}
		if len(workspace.Processes) != 0 {
			return fmt.Errorf("Workspace %s still has tracked processes", workspace.Path)
		}
		now := timestamp(service.now())
		workspace.Lifecycle = state.LifecycleAvailable
		workspace.OperationID = nil
		workspace.Assignment = nil
		workspace.UpdatedAt = now
		workspace.AvailableAt = &now
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

// CancelWorkspaceReturn restores ownership after cleanup fails.
func (service *Service) CancelWorkspaceReturn(ctx context.Context, assignmentID, failure string) (state.WorkspaceRecord, error) {
	if failure == "" {
		return state.WorkspaceRecord{}, errors.New("failure must not be empty")
	}
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, exists := findAssignment(current, assignmentID)
		if !exists {
			return fmt.Errorf("Assignment %s does not exist", assignmentID)
		}
		if workspace.Lifecycle != state.LifecycleReturning {
			return fmt.Errorf("Workspace %s is %s, expected returning", workspace.Path, workspace.Lifecycle)
		}
		now := timestamp(service.now())
		workspace.Lifecycle = state.LifecycleAssigned
		workspace.UpdatedAt = now
		workspace.AvailableAt = nil
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
