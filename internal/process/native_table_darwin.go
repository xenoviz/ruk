//go:build darwin

package process

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Darwin temporarily uses one bounded ps snapshot while the libproc-backed
// implementation is completed. Unlike polling, this executes once per safety
// decision and propagates every enumeration failure.
func snapshotPlatform(ctx context.Context) ([]Entry, error) {
	output, err := exec.CommandContext(ctx, "/bin/ps", "-e", "-o", "pid=", "-o", "ppid=", "-o", "pgid=", "-o", "state=").Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("snapshot Darwin process table: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	entries := make([]Entry, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse Darwin process row %q", line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("parse Darwin process ID: %w", err)
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse Darwin parent process ID: %w", err)
		}
		group, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("parse Darwin process group ID: %w", err)
		}
		if fields[3] != "Z" {
			entries = append(entries, Entry{PID: pid, ParentPID: parent, GroupID: group})
		}
	}
	return entries, nil
}
