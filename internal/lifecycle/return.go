package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/xenoviz/ruk/internal/state"
)

// ReturnOptions fences recovery against the exact state that selected it.
type ReturnOptions struct {
	RequireExpiredBy       string
	AcquisitionOperationID string
	ExpectedUpdatedAt      string
}

// BeginWorkspaceReturn retains the original optional acquisition-operation
// contract for ordinary callers. Recovery callers should use
// BeginWorkspaceReturnWithOptions so expiry and update fences are explicit.
func (service *Service) BeginWorkspaceReturn(ctx context.Context, assignmentID string, acquisitionOperationID ...string) (state.WorkspaceRecord, error) {
	if len(acquisitionOperationID) > 1 {
		return state.WorkspaceRecord{}, errors.New("acquisition operation may be supplied at most once")
	}
	options := ReturnOptions{}
	if len(acquisitionOperationID) == 1 {
		options.AcquisitionOperationID = acquisitionOperationID[0]
	}
	return service.BeginWorkspaceReturnWithOptions(ctx, assignmentID, options)
}

// BeginWorkspaceReturnWithOptions publishes the returning fence atomically.
func (service *Service) BeginWorkspaceReturnWithOptions(ctx context.Context, assignmentID string, options ReturnOptions) (state.WorkspaceRecord, error) {
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, exists := findAssignment(current, assignmentID)
		if !exists {
			return fmt.Errorf("Assignment %s does not exist", assignmentID)
		}
		// A retry after the return transition has already been published must
		// not revalidate an old expiry/update fence or rewrite its timestamp.
		if workspace.Lifecycle == state.LifecycleReturning {
			result = workspace
			return nil
		}
		if workspace.Lifecycle != state.LifecycleAssigned {
			return fmt.Errorf("Workspace %s is %s, expected assigned", workspace.Path, workspace.Lifecycle)
		}

		if workspace.OperationID != nil {
			if options.AcquisitionOperationID == "" || *workspace.OperationID != options.AcquisitionOperationID {
				return fmt.Errorf("Assignment %s acquisition is still in progress", assignmentID)
			}
		} else if options.AcquisitionOperationID != "" {
			return fmt.Errorf("Assignment %s acquisition operation does not match", assignmentID)
		}
		if options.ExpectedUpdatedAt != "" && workspace.UpdatedAt != options.ExpectedUpdatedAt {
			return fmt.Errorf("Assignment %s changed before collection", assignmentID)
		}
		if options.RequireExpiredBy != "" {
			expiry, err := parseTimestamp(workspace.Assignment.ExpiresAt)
			if err != nil {
				return err
			}
			cutoff, err := parseTimestamp(options.RequireExpiredBy)
			if err != nil {
				return fmt.Errorf("requireExpiredBy: %w", err)
			}
			if expiry.After(cutoff) {
				return fmt.Errorf("Assignment %s was renewed before collection", assignmentID)
			}
		}
		workspace.Lifecycle = state.LifecycleReturning
		workspace.Failure = nil
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
		// A recovery return may have crossed an acquisition handoff. Keep its
		// marker intact so cancellation cannot make that handoff releasable.
		acquisitionOperationID := workspace.OperationID
		now := timestamp(service.now())
		workspace.Lifecycle = state.LifecycleAssigned
		workspace.OperationID = acquisitionOperationID
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
