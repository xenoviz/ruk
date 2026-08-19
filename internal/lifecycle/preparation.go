// Package lifecycle owns durable workspace and assignment transitions.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

// Store is the state transaction boundary used by lifecycle operations.
type Store interface {
	Update(ctx context.Context, mutate func(*state.State) error) error
}

// Options injects deterministic time and identifier sources.
type Options struct {
	Now   func() time.Time
	NewID func() string
}

// Service applies lifecycle transitions through one Store.
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
}

// New creates a lifecycle Service.
func New(store Store, options Options) *Service {
	if store == nil {
		panic("lifecycle: nil store")
	}
	if options.Now == nil {
		panic("lifecycle: nil clock")
	}
	if options.NewID == nil {
		panic("lifecycle: nil ID generator")
	}
	return &Service{
		store: store,
		now:   options.Now,
		newID: options.NewID,
	}
}

// BeginPreparation records a newly managed worktree before external preparation starts.
func (service *Service) BeginPreparation(ctx context.Context, workspacePath, branch string) (state.WorkspaceRecord, error) {
	if branch == "" {
		return state.WorkspaceRecord{}, errors.New("branch must not be empty")
	}
	resolved, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return state.WorkspaceRecord{}, fmt.Errorf("resolve workspace path: %w", err)
	}
	key, err := state.TreeKey(resolved)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	now := timestamp(service.now())
	operationID := service.newID()
	workspace := state.WorkspaceRecord{
		Path:        resolved,
		Managed:     true,
		Branch:      branch,
		Lifecycle:   state.LifecyclePreparing,
		OperationID: &operationID,
		Assignment:  nil,
		Processes:   []state.TrackedProcessRecord{},
		CreatedAt:   now,
		UpdatedAt:   now,
		AvailableAt: nil,
		Failure:     nil,
	}
	err = service.store.Update(ctx, func(current *state.State) error {
		if _, exists := current.Workspaces[key]; exists {
			return fmt.Errorf("Workspace %s is already managed", resolved)
		}
		current.Workspaces[key] = workspace
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return workspace, nil
}

// MarkAvailable completes one preparation and publishes the worktree as pool capacity.
func (service *Service) MarkAvailable(ctx context.Context, workspacePath, operationID string) (state.WorkspaceRecord, error) {
	return service.finishPreparation(ctx, workspacePath, operationID, "", nil)
}

// MarkFailed retains a failed preparation for safe inspection and garbage collection.
func (service *Service) MarkFailed(ctx context.Context, workspacePath, operationID, failure string) (state.WorkspaceRecord, error) {
	return service.MarkFailedRetainingProcess(ctx, workspacePath, operationID, failure, nil)
}

// MarkFailedRetainingProcess atomically records an unsafe installer boundary
// while transitioning preparation to failed. This is the lifecycle fallback
// when the installer's earlier registration writes could not be completed.
func (service *Service) MarkFailedRetainingProcess(ctx context.Context, workspacePath, operationID, failure string, retained *state.TrackedProcessRecord) (state.WorkspaceRecord, error) {
	if failure == "" {
		return state.WorkspaceRecord{}, errors.New("failure must not be empty")
	}
	return service.finishPreparation(ctx, workspacePath, operationID, failure, retained)
}

func (service *Service) finishPreparation(ctx context.Context, workspacePath, operationID, failure string, retained *state.TrackedProcessRecord) (state.WorkspaceRecord, error) {
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
		if workspace.OperationID == nil || *workspace.OperationID != operationID {
			return errors.New("Preparation operation does not match")
		}
		now := nextWorkspaceTimestamp(workspace.UpdatedAt, service.now())
		if failure == "" && len(workspace.Processes) != 0 {
			return errors.New("workspace still has tracked processes")
		}
		if retained != nil {
			if err := appendRetainedProcess(&workspace, *retained); err != nil {
				return err
			}
		}
		workspace.OperationID = nil
		workspace.UpdatedAt = now
		if failure == "" {
			workspace.Lifecycle = state.LifecycleAvailable
			workspace.AvailableAt = &now
		} else {
			workspace.Lifecycle = state.LifecycleFailed
			workspace.Failure = &failure
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

func timestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}
