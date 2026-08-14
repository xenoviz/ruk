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
	"syscall"
	"time"

	"github.com/xenoviz/ruk/internal/lock"
)

const linuxClockTicks = uint64(100)

func inspectPlatform(_ context.Context, pid int) (lock.ProcessState, error) {
	processPath := strconv.Itoa(pid)
	if pid == os.Getpid() {
		processPath = "self"
	}
	stat, err := os.ReadFile(filepath.Join("/proc", processPath, "stat"))
	if errors.Is(err, os.ErrNotExist) {
		probeErr := syscall.Kill(pid, 0)
		switch {
		case probeErr == nil, errors.Is(probeErr, syscall.EPERM):
			return unavailableIdentity(pid, errors.New("live process is absent from /proc"))
		case errors.Is(probeErr, syscall.ESRCH):
			return lock.ProcessState{}, nil
		default:
			return unavailableIdentity(pid, probeErr)
		}
	}
	if err != nil {
		return unavailableIdentity(pid, err)
	}
	startTicks, err := parseLinuxStartTicks(string(stat))
	if err != nil {
		return unavailableIdentity(pid, err)
	}
	procStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return unavailableIdentity(pid, err)
	}
	bootTime, err := parseLinuxBootTime(string(procStat))
	if err != nil {
		return unavailableIdentity(pid, err)
	}
	started := time.Unix(bootTime+int64(startTicks/linuxClockTicks), 0)
	return lock.ProcessState{
		Alive:         true,
		IdentityKnown: true,
		Identity:      formatPOSIXIdentity(started),
	}, nil
}

func parseLinuxStartTicks(stat string) (uint64, error) {
	commandEnd := strings.LastIndex(stat, ") ")
	if commandEnd < 0 {
		return 0, errors.New("malformed /proc process stat")
	}
	fields := strings.Fields(stat[commandEnd+2:])
	if len(fields) <= 19 {
		return 0, errors.New("incomplete /proc process stat")
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc process start time: %w", err)
	}
	return startTicks, nil
}

func parseLinuxBootTime(stat string) (int64, error) {
	for _, line := range strings.Split(stat, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		bootTime, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse Linux boot time: %w", err)
		}
		return bootTime, nil
	}
	return 0, errors.New("Linux boot time is unavailable")
}
