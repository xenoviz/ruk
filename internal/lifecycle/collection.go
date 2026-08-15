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
		if !abandonedPreparation && workspace.OperationID != nil {
			result = workspace
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
		result = workspace
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
		result = workspace
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
		result = workspace
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
