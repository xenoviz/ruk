package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestResolveEntrypointFollowsSymlinkToDistributionTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "native", "ruk")
	link := filepath.Join(root, "bin", "ruk")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := resolveEntrypoint(func() (string, error) { return link, nil })
	if err != nil {
		t.Fatalf("resolveEntrypoint() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved entrypoint = %q, want %q", got, want)
	}
}

func TestResolveEntrypointFailsClosedWhenSymlinkResolutionFails(t *testing.T) {
	_, err := resolveEntrypoint(func() (string, error) { return filepath.Join(t.TempDir(), "missing"), nil })
	if err == nil || !strings.Contains(err.Error(), "resolve executable symlinks") {
		t.Fatalf("error = %v, want symlink resolution failure", err)
	}
}

func TestResolveEntrypointNormalizesRelativeExecutablePath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "native", "ruk")
	link := filepath.Join(root, "bin", "ruk")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Chdir(root)
	got, err := resolveEntrypoint(func() (string, error) { return filepath.Join("bin", "ruk"), nil })
	if err != nil {
		t.Fatalf("resolveEntrypoint() error = %v", err)
	}
	want, err := filepath.Abs(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved entrypoint = %q, want absolute %q", got, want)
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
