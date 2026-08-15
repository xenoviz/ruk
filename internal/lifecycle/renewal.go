package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

// RenewAssignment extends one assigned lease. A stale expectedRenewedAt fence is a no-op.
func (service *Service) RenewAssignment(ctx context.Context, assignmentID string, expiresAt time.Time, expectedRenewedAt *string) (state.WorkspaceRecord, error) {
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, exists := findAssignment(current, assignmentID)
		if !exists {
			return fmt.Errorf("Assignment %s does not exist", assignmentID)
		}
		if workspace.Lifecycle != state.LifecycleAssigned {
			return fmt.Errorf("Workspace %s is %s, expected assigned", workspace.Path, workspace.Lifecycle)
		}
		if expectedRenewedAt != nil && workspace.Assignment.RenewedAt != *expectedRenewedAt {
			result = cloneWorkspace(workspace)
			return nil
		}

		renewedAtValue := service.now().UTC().Truncate(time.Millisecond)
		expiresAtValue := expiresAt.UTC().Truncate(time.Millisecond)
		if !expiresAtValue.After(renewedAtValue) {
			return errors.New("expiresAt must be after now")
		}
		renewedAt := timestamp(renewedAtValue)
		workspace.Assignment.RenewedAt = renewedAt
		workspace.Assignment.ExpiresAt = timestamp(expiresAtValue)
		workspace.Assignment.LeaseDurationMinutes = expiresAtValue.Sub(renewedAtValue).Minutes()
		workspace.Assignment.LastActivityAt = renewedAt
		workspace.UpdatedAt = renewedAt
		current.Workspaces[key] = workspace
		result = cloneWorkspace(workspace)
		return nil
	})
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	return result, nil
}
