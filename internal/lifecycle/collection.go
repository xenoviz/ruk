package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/xenoviz/ruk/internal/state"
)

// BeginWorkspaceCollection fences a safe workspace before external Git
// removal. An interrupted existing collection is returned unchanged.
func (service *Service) BeginWorkspaceCollection(ctx context.Context, workspacePath, expectedUpdatedAt string) (state.WorkspaceRecord, error) {
	resolved, key, err := collectionKey(workspacePath)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	var result state.WorkspaceRecord
	err = service.store.Update(ctx, func(current *state.State) error {
		workspace, exists := current.Workspaces[key]
		if !exists {
			return fmt.Errorf("Workspace %s is not managed", resolved)
		}
		abandonedPreparation := workspace.Lifecycle == state.LifecyclePreparing
		if !abandonedPreparation && workspace.Lifecycle != state.LifecycleAvailable && workspace.Lifecycle != state.LifecycleFailed {
			return fmt.Errorf("Workspace %s is not safe to collect", resolved)
		}
		if workspace.UpdatedAt != expectedUpdatedAt {
			return fmt.Errorf("Workspace %s changed before collection", resolved)
		}
		if len(workspace.Processes) != 0 {
			return fmt.Errorf("Workspace %s still has tracked processes", resolved)
		}
		if !abandonedPreparation && workspace.OperationID != nil {
			result = cloneWorkspace(workspace)
			return nil
		}
		if abandonedPreparation {
			failure := "Workspace preparation was abandoned"
			workspace.Lifecycle = state.LifecycleFailed
			workspace.Failure = &failure
		}
		operationID := service.newID()
		workspace.OperationID = &operationID
		workspace.UpdatedAt = timestamp(service.now())
		current.Workspaces[key] = workspace
		result = cloneWorkspace(workspace)
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

// RemoveUnassignedProcess removes one exact tracked process from a failed or
// abandoned-preparation workspace. The lifecycle and operation fences are
// checked inside the state transaction so GC cannot remove a record belonging
// to a newer workspace operation.
func (service *Service) RemoveUnassignedProcess(ctx context.Context, workspacePath string, expectedLifecycle state.WorkspaceLifecycle, expectedOperationID *string, expectedUpdatedAt string, pid int64, startedAt string) (state.WorkspaceRecord, error) {
	resolved, key, err := collectionKey(workspacePath)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	if expectedLifecycle != state.LifecyclePreparing && expectedLifecycle != state.LifecycleFailed {
		return state.WorkspaceRecord{}, errors.New("workspace is not an unassigned preparation or failed workspace")
	}
	var result state.WorkspaceRecord
	err = service.store.Update(ctx, func(current *state.State) error {
		workspace, exists := current.Workspaces[key]
		if !exists {
			return fmt.Errorf("Workspace %s is not managed", resolved)
		}
		if workspace.Lifecycle != expectedLifecycle {
			return fmt.Errorf("Workspace %s changed before process cleanup", resolved)
		}
		if workspace.Assignment != nil {
			return fmt.Errorf("Workspace %s is assigned", resolved)
		}
		if workspace.UpdatedAt != expectedUpdatedAt {
			return fmt.Errorf("Workspace %s changed before process cleanup", resolved)
		}
		if !sameCollectionOperation(workspace.OperationID, expectedOperationID) {
			return errors.New("Workspace operation changed before process cleanup")
		}
		index := -1
		for candidate := range workspace.Processes {
			tracked := workspace.Processes[candidate]
			if tracked.PID == pid && tracked.StartedAt == startedAt {
				index = candidate
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("Process %d with identity %s is not tracked", pid, startedAt)
		}
		workspace.Processes = append(workspace.Processes[:index], workspace.Processes[index+1:]...)
		workspace.UpdatedAt = timestamp(service.now())
		current.Workspaces[key] = workspace
		result = cloneWorkspace(workspace)
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

// CancelWorkspaceCollection clears an exact collection fence after external
// cleanup fails, making the record retryable.
func (service *Service) CancelWorkspaceCollection(ctx context.Context, workspacePath, operationID string) (state.WorkspaceRecord, error) {
	resolved, key, err := collectionKey(workspacePath)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	var result state.WorkspaceRecord
	err = service.store.Update(ctx, func(current *state.State) error {
		workspace, exists := current.Workspaces[key]
		if !exists {
			return fmt.Errorf("Workspace %s is not managed", resolved)
		}
		if workspace.OperationID == nil || *workspace.OperationID != operationID {
			return errors.New("Collection operation does not match")
		}
		workspace.OperationID = nil
		cancelledAt := timestamp(service.now())
		workspace.UpdatedAt = cancelledAt
		if workspace.Lifecycle == state.LifecycleAvailable {
			workspace.AvailableAt = &cancelledAt
		}
		current.Workspaces[key] = workspace
		result = cloneWorkspace(workspace)
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

// DeleteWorkspaceRecord removes the exact safely collected state record.
func (service *Service) DeleteWorkspaceRecord(ctx context.Context, workspacePath, operationID string) (state.WorkspaceRecord, error) {
	resolved, key, err := collectionKey(workspacePath)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	var result state.WorkspaceRecord
	err = service.store.Update(ctx, func(current *state.State) error {
		workspace, exists := current.Workspaces[key]
		if !exists {
			return fmt.Errorf("Workspace %s is not managed", resolved)
		}
		if workspace.Lifecycle != state.LifecycleAvailable && workspace.Lifecycle != state.LifecycleFailed {
			return fmt.Errorf("Workspace %s is not safe to collect", resolved)
		}
		if workspace.Assignment != nil || len(workspace.Processes) != 0 {
			return fmt.Errorf("Workspace %s is still owned", resolved)
		}
		if workspace.OperationID == nil || *workspace.OperationID != operationID {
			return errors.New("Collection operation does not match")
		}
		delete(current.Workspaces, key)
		result = cloneWorkspace(workspace)
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}

func collectionKey(workspacePath string) (string, string, error) {
	resolved, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace path: %w", err)
	}
	key, err := state.TreeKey(resolved)
	if err != nil {
		return "", "", err
	}
	return resolved, key, nil
}

func sameCollectionOperation(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
