package process

import "context"

// NativeTable captures process relationships using the operating system's
// process interfaces. It never starts a polling helper on Windows.
type NativeTable struct{}

// Snapshot returns one bounded view of the host process table.
func (NativeTable) Snapshot(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return snapshotPlatform(ctx)
}

// ExitedGroupLeader reports whether pid is a zombie that leads its own process
// group. Callers must own pid as an unreaped child so the PID cannot be reused.
func (NativeTable) ExitedGroupLeader(ctx context.Context, pid int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return exitedGroupLeaderPlatform(ctx, pid)
}
