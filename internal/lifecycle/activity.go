package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

var keeperIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// BeginAssignmentActivity registers one heartbeat keeper and records activity.
func (service *Service) BeginAssignmentActivity(ctx context.Context, assignmentID, keeperID string, duration time.Duration) (state.WorkspaceRecord, error) {
	if !keeperIDPattern.MatchString(keeperID) {
		return state.WorkspaceRecord{}, errors.New("keeperId must be a UUID")
	}
	if duration <= 0 {
		return state.WorkspaceRecord{}, errors.New("duration must be positive")
	}
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, err := assignedWorkspace(current, assignmentID)
		if err != nil {
			return err
		}
		for _, keeper := range workspace.Assignment.LeaseKeepers {
			if keeper.ID == keeperID {
				return fmt.Errorf("Lease keeper %s is already active", keeperID)
			}
		}
		observedAt := service.now().UTC().Truncate(time.Millisecond)
		renewedAt, err := parseTimestamp(workspace.Assignment.RenewedAt)
		if err != nil {
			return err
		}
		effectiveNow := laterTime(observedAt, renewedAt)
		active, err := activeKeepers(workspace.Assignment.LeaseKeepers, effectiveNow, "")
		if err != nil {
			return err
		}
		workspace.Assignment.LeaseKeepers = active
		workspace.Assignment.LeaseKeepers = append(workspace.Assignment.LeaseKeepers, state.LeaseKeeperRecord{
			ID:          keeperID,
			HeartbeatAt: timestamp(effectiveNow),
			ValidUntil:  timestamp(effectiveNow.Add(duration)),
		})
		if err := recordActivity(&workspace, effectiveNow); err != nil {
			return err
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

// RefreshAssignmentActivity advances one keeper without overwriting a newer explicit renewal.
func (service *Service) RefreshAssignmentActivity(ctx context.Context, assignmentID, keeperID string, validUntil time.Time) (state.WorkspaceRecord, error) {
	if !keeperIDPattern.MatchString(keeperID) {
		return state.WorkspaceRecord{}, errors.New("keeperId must be a UUID")
	}
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, err := assignedWorkspace(current, assignmentID)
		if err != nil {
			return err
		}
		observedAt := service.now().UTC().Truncate(time.Millisecond)
		requestedValidUntil := validUntil.UTC().Truncate(time.Millisecond)
		if !requestedValidUntil.After(observedAt) {
			return errors.New("validUntil must be after now")
		}
		renewedAt, err := parseTimestamp(workspace.Assignment.RenewedAt)
		if err != nil {
			return err
		}
		effectiveNow := laterTime(observedAt, renewedAt)
		effectiveValidUntil := effectiveNow.Add(requestedValidUntil.Sub(observedAt))
		keeperFound := false
		for index := range workspace.Assignment.LeaseKeepers {
			keeper := &workspace.Assignment.LeaseKeepers[index]
			if keeper.ID != keeperID {
				continue
			}
			keeperFound = true
			keeper.HeartbeatAt = timestamp(effectiveNow)
			currentValidUntil, err := parseTimestamp(keeper.ValidUntil)
			if err != nil {
				return err
			}
			if effectiveValidUntil.After(currentValidUntil) {
				keeper.ValidUntil = timestamp(effectiveValidUntil)
			}
		}
		if !keeperFound {
			return fmt.Errorf("Lease keeper %s is not active", keeperID)
		}
		active, err := activeKeepers(workspace.Assignment.LeaseKeepers, effectiveNow, keeperID)
		if err != nil {
			return err
		}
		workspace.Assignment.LeaseKeepers = active
		if err := recordActivity(&workspace, effectiveNow); err != nil {
			return err
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

// FinishAssignmentActivity removes one keeper while preserving monotonic renewal time.
func (service *Service) FinishAssignmentActivity(ctx context.Context, assignmentID, keeperID string) (state.WorkspaceRecord, error) {
	if !keeperIDPattern.MatchString(keeperID) {
		return state.WorkspaceRecord{}, errors.New("keeperId must be a UUID")
	}
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, err := assignedWorkspace(current, assignmentID)
		if err != nil {
			return err
		}
		renewedAt, err := parseTimestamp(workspace.Assignment.RenewedAt)
		if err != nil {
			return err
		}
		completedAt := laterTime(service.now().UTC().Truncate(time.Millisecond), renewedAt)
		keeperFound := false
		remaining := make([]state.LeaseKeeperRecord, 0, len(workspace.Assignment.LeaseKeepers))
		for _, keeper := range workspace.Assignment.LeaseKeepers {
			if keeper.ID == keeperID {
				keeperFound = true
				continue
			}
			validUntil, err := parseTimestamp(keeper.ValidUntil)
			if err != nil {
				return err
			}
			if validUntil.After(completedAt) {
				remaining = append(remaining, keeper)
			}
		}
		if !keeperFound {
			return fmt.Errorf("Lease keeper %s is not active", keeperID)
		}
		workspace.Assignment.LeaseKeepers = remaining
		if err := recordActivity(&workspace, completedAt); err != nil {
			return err
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

func assignedWorkspace(current *state.State, assignmentID string) (string, state.WorkspaceRecord, error) {
	key, workspace, exists := findAssignment(current, assignmentID)
	if !exists {
		return "", state.WorkspaceRecord{}, fmt.Errorf("Assignment %s does not exist", assignmentID)
	}
	if workspace.Lifecycle != state.LifecycleAssigned {
		return "", state.WorkspaceRecord{}, fmt.Errorf("Workspace %s is %s, expected assigned", workspace.Path, workspace.Lifecycle)
	}
	return key, workspace, nil
}

func recordActivity(workspace *state.WorkspaceRecord, observedAt time.Time) error {
	minutes := workspace.Assignment.LeaseDurationMinutes
	const maxDuration = time.Duration(1<<63 - 1)
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 || minutes > float64(maxDuration)/float64(time.Minute) {
		return errors.New("lease duration is invalid")
	}
	duration := time.Duration(minutes * float64(time.Minute))
	now := timestamp(observedAt)
	workspace.Assignment.RenewedAt = now
	workspace.Assignment.ExpiresAt = timestamp(observedAt.Add(duration))
	workspace.Assignment.LastActivityAt = now
	workspace.UpdatedAt = now
	return nil
}

func activeKeepers(keepers []state.LeaseKeeperRecord, observedAt time.Time, retainedID string) ([]state.LeaseKeeperRecord, error) {
	active := make([]state.LeaseKeeperRecord, 0, len(keepers))
	for _, keeper := range keepers {
		validUntil, err := parseTimestamp(keeper.ValidUntil)
		if err != nil {
			return nil, err
		}
		if keeper.ID == retainedID || validUntil.After(observedAt) {
			active = append(active, keeper)
		}
	}
	return active, nil
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse lifecycle timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func laterTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return right
	}
	return left
}
