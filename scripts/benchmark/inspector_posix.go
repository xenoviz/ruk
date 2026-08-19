//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func inspectProcesses(roots []int) (processReport, error) {
	if runtime.GOOS == "linux" {
		return inspectLinux(roots)
	}
	return inspectPS(roots)
}

func inspectLinux(roots []int) (processReport, error) {
	entries := make(map[int]processEntry)
	directories, err := os.ReadDir("/proc")
	if err != nil {
		return processReport{}, fmt.Errorf("read /proc: %w", err)
	}
	for _, directory := range directories {
		pid, parseErr := strconv.Atoi(directory.Name())
		if parseErr != nil || pid <= 0 {
			continue
		}
		status, readErr := os.ReadFile(filepath.Join("/proc", directory.Name(), "status"))
		if readErr != nil {
			continue
		}
		parent, rss, name := parseLinuxStatus(status)
		entries[pid] = processEntry{record: processRecord{PID: pid, ParentPID: parent, Name: name, RSSBytes: rss}}
	}
	return treeReport(roots, entries), nil
}

func parseLinuxStatus(status []byte) (parent int, rss uint64, name string) {
	for scanner := bufio.NewScanner(bytes.NewReader(status)); scanner.Scan(); {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "Name":
			name = value
		case "PPid":
			parent, _ = strconv.Atoi(value)
		case "VmRSS":
			fields := strings.Fields(value)
			if len(fields) > 0 {
				kilobytes, _ := strconv.ParseUint(fields[0], 10, 64)
				rss = kilobytes * 1024
			}
		}
	}
	return parent, rss, name
}

func inspectPS(roots []int) (processReport, error) {
	command := exec.Command("/bin/ps", "-axo", "pid=,ppid=,rss=,comm=")
	output, err := command.Output()
	if err != nil {
		return processReport{}, fmt.Errorf("inspect process table: %w", err)
	}
	entries := make(map[int]processEntry)
	for scanner := bufio.NewScanner(bytes.NewReader(output)); scanner.Scan(); {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		rss, rssErr := strconv.ParseUint(fields[2], 10, 64)
		if pidErr != nil || parentErr != nil || rssErr != nil {
			continue
		}
		entries[pid] = processEntry{record: processRecord{
			PID: pid, ParentPID: parent, Name: strings.Join(fields[3:], " "), RSSBytes: rss * 1024,
		}}
	}
	return treeReport(roots, entries), nil
}
