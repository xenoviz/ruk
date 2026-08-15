// Command process-inspector reports native process-tree measurements for the
// benchmark harness. It intentionally has no third-party dependencies and is
// compiled for the host by scripts/benchmark-runtime.ts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

type processRecord struct {
	PID       int    `json:"pid"`
	ParentPID int    `json:"parentPid"`
	Name      string `json:"name"`
	RSSBytes  uint64 `json:"rssBytes"`
}

type processEntry struct {
	record processRecord
}

type processReport struct {
	Processes []processRecord `json:"processes"`
}

func treeReport(roots []int, entries map[int]processEntry) processReport {
	children := make(map[int][]int)
	for pid, value := range entries {
		children[value.record.ParentPID] = append(children[value.record.ParentPID], pid)
	}
	seen := make(map[int]bool)
	report := processReport{}
	var visit func(int)
	visit = func(pid int) {
		if seen[pid] {
			return
		}
		value, ok := entries[pid]
		if !ok {
			return
		}
		seen[pid] = true
		report.Processes = append(report.Processes, value.record)
		for _, child := range children[pid] {
			visit(child)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	return report
}

func main() {
	var pids pidFlags
	flag.Var(&pids, "pid", "root process ID (repeatable)")
	flag.Parse()
	if len(pids) == 0 {
		fail("at least one --pid is required")
	}
	report, err := inspectProcesses([]int(pids))
	if err != nil {
		fail(err.Error())
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fail(err.Error())
	}
}

type pidFlags []int

func (values *pidFlags) String() string { return fmt.Sprint([]int(*values)) }

func (values *pidFlags) Set(value string) error {
	var pid int
	if _, err := fmt.Sscanf(value, "%d", &pid); err != nil || pid <= 0 {
		return fmt.Errorf("invalid pid %q", value)
	}
	*values = append(*values, pid)
	return nil
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
