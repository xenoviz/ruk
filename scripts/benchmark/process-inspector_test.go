package main

import (
	"encoding/json"
	"testing"
)

func TestTreeReportEncodesMissingRootsAsAnEmptyArray(t *testing.T) {
	report := treeReport([]int{42}, map[int]processEntry{})
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal process report: %v", err)
	}
	if got, want := string(encoded), `{"processes":[]}`; got != want {
		t.Fatalf("encoded report = %s, want %s", got, want)
	}
}

func TestTreeReportIncludesDescendantsOfAMissingRoot(t *testing.T) {
	report := treeReport([]int{42}, map[int]processEntry{
		99: {record: processRecord{PID: 99, ParentPID: 42, Name: "pwsh.exe", RSSBytes: 1024}},
	})
	if len(report.Processes) != 1 || report.Processes[0].PID != 99 {
		t.Fatalf("processes = %#v, want leaderless child 99", report.Processes)
	}
}
