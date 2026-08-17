package lock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGuardReleaseLogicallyRemovesCanonicalPathBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pool.lock")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create lock: %v", err)
	}
	if err := writeOwner(path, Owner{PID: 42, Hostname: "host", Token: "token", CreatedAt: "2026-01-01T00:00:00.000Z"}); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	if err := (&Guard{path: path, token: "token"}).Release(); err != nil {
		t.Fatalf("Release returned an error: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical lock still exists: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read lock parent: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("released tombstone was not cleaned: %v", entries)
	}
}

func TestGuardReleaseDoesNotMoveWhenReleasedPathIsOccupied(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pool.lock")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create lock: %v", err)
	}
	if err := writeOwner(path, Owner{PID: 42, Hostname: "host", Token: "token", CreatedAt: "2026-01-01T00:00:00.000Z"}); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	releasedPath := path + ".released-" + releaseToken("token")
	if err := os.MkdirAll(releasedPath, 0o700); err != nil {
		t.Fatalf("create occupied release path: %v", err)
	}
	if err := (&Guard{path: path, token: "token"}).Release(); err == nil {
		t.Fatal("Release unexpectedly succeeded with an occupied release path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("canonical lock was not preserved: %v", err)
	}
}
