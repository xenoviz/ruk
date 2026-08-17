package lifecycle

import (
	"errors"
	"fmt"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

func appendRetainedProcess(workspace *state.WorkspaceRecord, record state.TrackedProcessRecord) error {
	if workspace == nil {
		return errors.New("workspace is unavailable for retained process recovery")
	}
	if record.PID <= 0 || record.StartedAt == "" {
		return errors.New("retained process record is incomplete")
	}
	for _, tracked := range workspace.Processes {
		if tracked.PID != record.PID {
			continue
		}
		if tracked.StartedAt == record.StartedAt {
			return nil
		}
		return fmt.Errorf("process %d is already tracked with another identity", record.PID)
	}
	workspace.Processes = append(workspace.Processes, record)
	return nil
}

func nextWorkspaceTimestamp(previous string, observed time.Time) string {
	next := observed.UTC().Truncate(time.Millisecond)
	if parsed, err := time.Parse(time.RFC3339Nano, previous); err == nil && !next.After(parsed) {
		next = parsed.UTC().Truncate(time.Millisecond).Add(time.Millisecond)
	}
	return timestamp(next)
}
