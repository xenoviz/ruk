package process

import (
	"context"
	"errors"
	"strconv"
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

// descendantsCreatedAfterParents filters a parent-PID walk to processes whose
// creation identity is not earlier than their parent's. Windows never updates
// a process's recorded parent PID when that parent exits and reuses PIDs
// quickly, so an unrelated older process can name a tracked leader's PID as
// its parent. identities holds creation ticks as decimal strings; a link whose
// identities cannot be compared is followed, preserving the walk's result.
func descendantsCreatedAfterParents(entries []Entry, root int, pids []int, identities map[int]string) []int {
	children := make(map[int][]int)
	for _, entry := range entries {
		if entry.PID > 0 && entry.ParentPID > 0 {
			children[entry.ParentPID] = append(children[entry.ParentPID], entry.PID)
		}
	}
	kept := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if kept[child] || createdBefore(identities[child], identities[parent]) {
				continue
			}
			kept[child] = true
			queue = append(queue, child)
		}
	}
	result := make([]int, 0, len(pids))
	for _, pid := range pids {
		if kept[pid] {
			result = append(result, pid)
		}
	}
	return result
}

func createdBefore(child, parent string) bool {
	childTicks, childErr := strconv.ParseUint(child, 10, 64)
	parentTicks, parentErr := strconv.ParseUint(parent, 10, 64)
	return childErr == nil && parentErr == nil && childTicks < parentTicks
}
