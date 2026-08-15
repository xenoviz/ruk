package main

import (
	"bytes"
	"testing"
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
	if stdout.String() != "" || stderr.String() != "ruk: command is not implemented\n" {
		t.Fatalf("error stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
