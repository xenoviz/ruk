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

func TestGuardReleaseRefusesCanonicalSymlinkReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pool.lock")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create lock: %v", err)
	}
	if err := writeOwner(path, Owner{PID: 42, Hostname: "host", Token: "token", CreatedAt: "2026-01-01T00:00:00.000Z"}); err != nil {
		t.Fatalf("write owner: %v", err)
	}
	target := path + ".replacement-target"
	if err := os.Rename(path, target); err != nil {
		t.Fatalf("move original lock: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	err := (&Guard{path: path, token: "token"}).Release()
	if err == nil {
		t.Fatal("Release unexpectedly followed canonical symlink")
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("canonical replacement = %#v, %v; symlink was removed", info, statErr)
	}
	if _, statErr := os.Stat(filepath.Join(target, "owner.json")); statErr != nil {
		t.Fatalf("replacement target was removed: %v", statErr)
	}
}

func TestGuardReleaseRetryNeverDeletesMismatchedTombstone(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pool.lock")
	releasedPath := path + ".released-" + releaseToken("replacement")
	if err := os.MkdirAll(releasedPath, 0o700); err != nil {
		t.Fatalf("create released lock: %v", err)
	}
	if err := writeOwner(releasedPath, Owner{PID: 43, Hostname: "host", Token: "replacement", CreatedAt: "2026-01-01T00:00:00.000Z"}); err != nil {
		t.Fatalf("write replacement owner: %v", err)
	}
	guard := &Guard{path: path, token: "original", releasedPath: releasedPath}
	if err := guard.Release(); err == nil {
		t.Fatal("Release unexpectedly cleaned a mismatched tombstone")
	}
	if _, err := os.Stat(releasedPath); err != nil {
		t.Fatalf("replacement tombstone was removed: %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("second Release returned an error after abandoning mismatched tombstone: %v", err)
	}
	if _, err := os.Stat(releasedPath); err != nil {
		t.Fatalf("replacement tombstone was removed on retry: %v", err)
	}
}

func TestCleanupReleasedTombstonesLeavesCanonicalAndUnrelatedEntries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pool.lock")
	matching := path + ".released-" + releaseToken("token")
	if err := os.MkdirAll(matching, 0o700); err != nil {
		t.Fatalf("create matching tombstone: %v", err)
	}
	if err := writeOwner(matching, Owner{PID: 42, Hostname: "host", Token: "token", CreatedAt: "2026-01-01T00:00:00.000Z"}); err != nil {
		t.Fatalf("write matching owner: %v", err)
	}
	mismatched := path + ".released-deadbeef"
	if err := os.MkdirAll(mismatched, 0o700); err != nil {
		t.Fatalf("create mismatched tombstone: %v", err)
	}
	if err := writeOwner(mismatched, Owner{PID: 43, Hostname: "host", Token: "other", CreatedAt: "2026-01-01T00:00:00.000Z"}); err != nil {
		t.Fatalf("write mismatched owner: %v", err)
	}
	malformed := path + ".released-" + releaseToken("malformed")
	if err := os.MkdirAll(malformed, 0o700); err != nil {
		t.Fatalf("create malformed tombstone: %v", err)
	}
	unrelated := filepath.Join(root, "other.lock.released-deadbeef")
	if err := os.MkdirAll(unrelated, 0o700); err != nil {
		t.Fatalf("create unrelated tombstone: %v", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create canonical lock: %v", err)
	}
	cleanupReleasedTombstones(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("canonical lock was touched: %v", err)
	}
	if _, err := os.Stat(matching); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matching tombstone remains: %v", err)
	}
	if _, err := os.Stat(mismatched); err != nil {
		t.Fatalf("mismatched tombstone was touched: %v", err)
	}
	if _, err := os.Stat(malformed); err != nil {
		t.Fatalf("malformed tombstone was touched: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated tombstone was touched: %v", err)
	}
}
