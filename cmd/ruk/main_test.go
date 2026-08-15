package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
)

func TestRunMainPreservesVersionAndErrorBoundary(t *testing.T) {
	originalVersion := version
	version = "0.3.0-test"
	t.Cleanup(func() { version = originalVersion })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runMain([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("version exit code = %d, want 0", code)
	}
	if stdout.String() != "0.3.0-test\n" || stderr.String() != "" {
		t.Fatalf("version stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMain([]string{"unknown"}, &stdout, &stderr); code != 1 {
		t.Fatalf("error exit code = %d, want 1", code)
	}
	if stdout.String() != "" || stderr.String() != "ruk: Unknown command unknown. Run ruk --help.\n" {
		t.Fatalf("error stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunMainUsesProductionRuntimeDefaults(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	t.Chdir(repository)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runMain([]string{"gc", "--max-age", "0", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("gc exit code = %d, stderr=%q", code, stderr.String())
	}
	if stdout.String() != "{\"status\":\"planned\",\"removed\":[],\"expired\":[]}\n" || stderr.String() != "" {
		t.Fatalf("gc stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunMainWritesOneStructuredErrorWhenJSONIsRequested(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runMain([]string{"status", "--unknown", "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var record cli.ErrorRecord
	if err := json.Unmarshal(stderr.Bytes(), &record); err != nil {
		t.Fatalf("stderr = %q: %v", stderr.String(), err)
	}
	if record.Status != "error" || record.Code != cli.InvalidArgumentCode || record.Retryable {
		t.Fatalf("record = %#v", record)
	}
}
