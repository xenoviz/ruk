//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func snapshotPlatform(ctx context.Context) ([]Entry, error) {
	directories, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read Linux process table: %w", err)
	}
	entries := make([]Entry, 0, len(directories))
	for _, directory := range directories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(directory.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", directory.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read Linux process %d: %w", pid, err)
		}
		entry, state, err := parseLinuxProcessEntry(string(stat))
		if err != nil {
			return nil, fmt.Errorf("read Linux process %d: %w", pid, err)
		}
		if state != "Z" {
			entries = append(entries, entry)
		}
	}
	currentPID := os.Getpid()
	for _, entry := range entries {
		if entry.PID == currentPID {
			return entries, nil
		}
	}
	// Some container runtimes expose a procfs mounted from an outer PID
	// namespace. /proc/self remains authoritative for this process even when
	// the numeric directory rows use the outer namespace identifiers.
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return nil, fmt.Errorf("read current Linux process: %w", err)
	}
	current, state, err := parseLinuxProcessEntry(string(stat))
	if err != nil {
		return nil, fmt.Errorf("read current Linux process: %w", err)
	}
	if state != "Z" {
		current.PID = currentPID
		entries = append(entries, current)
	}
	return entries, nil
}

func parseLinuxProcessEntry(stat string) (Entry, string, error) {
	commandEnd := strings.LastIndex(stat, ") ")
	if commandEnd < 0 {
		return Entry{}, "", errors.New("malformed /proc process stat")
	}
	pidFields := strings.Fields(stat[:commandEnd])
	fields := strings.Fields(stat[commandEnd+2:])
	if len(pidFields) == 0 || len(fields) < 4 {
		return Entry{}, "", errors.New("incomplete /proc process stat")
	}
	pid, err := strconv.Atoi(pidFields[0])
	if err != nil {
		return Entry{}, "", fmt.Errorf("parse process ID: %w", err)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return Entry{}, "", fmt.Errorf("parse parent process ID: %w", err)
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return Entry{}, "", fmt.Errorf("parse process group ID: %w", err)
	}
	return Entry{PID: pid, ParentPID: parent, GroupID: group}, fields[0], nil
}
