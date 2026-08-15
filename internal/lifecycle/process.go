package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/xenoviz/ruk/internal/state"
)

const maxSafeProcessID = int64(9_007_199_254_740_991)

// AddAssignmentProcess durably attaches one identity-fenced process to an
// assignment before the caller considers registration complete.
func (service *Service) AddAssignmentProcess(ctx context.Context, assignmentID string, record state.TrackedProcessRecord) (state.WorkspaceRecord, error) {
	if err := validateProcessRecord(record); err != nil {
		return state.WorkspaceRecord{}, err
	}
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, err := assignedWorkspace(current, assignmentID)
		if err != nil {
			return err
		}
		for _, tracked := range workspace.Processes {
			if tracked.PID == record.PID {
				return fmt.Errorf("Process %d is already tracked", record.PID)
			}
		}
		workspace.Processes = append(workspace.Processes, cloneProcessRecord(record))
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

// RemoveAssignmentProcess removes only the exact persisted PID identity. It is
// allowed during return so cleanup can drain records before publishing a slot.
func (service *Service) RemoveAssignmentProcess(ctx context.Context, assignmentID string, pid int64, startedAt string) (state.WorkspaceRecord, error) {
	var result state.WorkspaceRecord
	err := service.store.Update(ctx, func(current *state.State) error {
		key, workspace, exists := findAssignment(current, assignmentID)
		if !exists {
			return fmt.Errorf("Assignment %s does not exist", assignmentID)
		}
		if workspace.Lifecycle != state.LifecycleAssigned && workspace.Lifecycle != state.LifecycleReturning {
			return fmt.Errorf("Workspace %s is not assigned or returning", workspace.Path)
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

func validateProcessRecord(record state.TrackedProcessRecord) error {
	if !safePositiveProcessID(record.PID) {
		return errors.New("pid must be a positive safe integer")
	}
	if record.StartedAt == "" {
		return errors.New("startedAt must not be empty")
	}
	if record.Command != nil && len(record.Command) == 0 {
		return errors.New("command must not be empty")
	}
	if record.GroupID != nil && !safePositiveProcessID(*record.GroupID) {
		return errors.New("groupId must be a positive safe integer")
	}
	if record.SessionID != nil && !safePositiveProcessID(*record.SessionID) {
		return errors.New("sessionId must be a positive safe integer")
	}
	if (record.SessionID == nil) != (record.SessionStartedAt == nil) {
		return errors.New("sessionId and sessionStartedAt must be provided together")
	}
	if record.SessionStartedAt != nil && *record.SessionStartedAt == "" {
		return errors.New("sessionStartedAt must not be empty")
	}
	if record.TerminalID != nil && (*record.TerminalID == "" || *record.TerminalID == "??") {
		return errors.New("terminalId must identify a controlling terminal")
	}
	return nil
}

func safePositiveProcessID(value int64) bool {
	return value > 0 && value <= maxSafeProcessID
}

func cloneProcessRecord(record state.TrackedProcessRecord) state.TrackedProcessRecord {
	copy := record
	copy.GroupID = cloneInt64(record.GroupID)
	copy.SessionID = cloneInt64(record.SessionID)
	copy.SessionStartedAt = cloneString(record.SessionStartedAt)
	copy.TerminalID = cloneString(record.TerminalID)
	copy.Command = append([]string(nil), record.Command...)
	return copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
