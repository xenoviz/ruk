package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteStateReplacesExistingHeartbeat(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "state.json")
	state := fixtureState{AssignmentID: "fixture", HeartbeatAt: "first"}
	if err := writeState(filename, state); err != nil {
		t.Fatal(err)
	}
	state.HeartbeatAt = "second"
	if err := writeState(filename, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"assignmentId\":\"fixture\",\"heartbeatAt\":\"second\"}\n" {
		t.Fatalf("unexpected state: %s", data)
	}
}
