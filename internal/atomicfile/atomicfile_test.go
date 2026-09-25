package atomicfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/atomicfile"
)

func TestReplaceWritesOwnerOnlyContentsWithoutLeavingTemporaryFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Replace(target, []byte("new\n"), "state"); err != nil {
		t.Fatalf("Replace returned an error: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new\n" {
		t.Fatalf("contents = %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want only the target", entries, err)
	}
}

func TestReplaceFailureRemovesTemporaryFileAndKeepsDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	// A non-empty directory at the destination makes the final rename fail.
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := atomicfile.Replace(target, []byte("new"), "state")
	if err == nil || !strings.Contains(err.Error(), "replace state "+target) {
		t.Fatalf("Replace error = %v, want replace failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "child")); statErr != nil {
		t.Fatalf("destination changed after failed replace: %v", statErr)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want the temporary file removed", entries, readErr)
	}
}
