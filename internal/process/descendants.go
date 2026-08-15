package process

import (
	"context"
	"errors"
)

// Entry is one process and its immediate parent from a platform snapshot.
type Entry struct {
	PID       int
	ParentPID int
	GroupID   int
}

// ProcessTable captures one bounded operating-system process snapshot.
type ProcessTable interface {
	Snapshot(ctx context.Context) ([]Entry, error)
}

// DescendantInspector checks process ancestry from a single stable snapshot.
type DescendantInspector struct {
	Table ProcessTable
}

// Exists reports whether root has a direct or transitive descendant.
func (inspector DescendantInspector) Exists(ctx context.Context, root int) (bool, error) {
	if root <= 0 {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if inspector.Table == nil {
		return false, errors.New("process: process table is unavailable")
	}
	entries, err := inspector.Table.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.PID > 0 && entry.PID != root && entry.GroupID == root {
			return true, nil
		}
	}
	ancestors := map[int]struct{}{root: {}}
	for {
		changed := false
		for _, entry := range entries {
			if entry.PID <= 0 || entry.PID == root {
				continue
			}
			if _, known := ancestors[entry.PID]; known {
				continue
			}
			if _, parentKnown := ancestors[entry.ParentPID]; !parentKnown {
				continue
			}
			ancestors[entry.PID] = struct{}{}
			changed = true
		}
		if !changed {
			break
		}
	}
	return len(ancestors) > 1, nil
}
