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
