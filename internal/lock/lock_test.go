package lock_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lockpkg "github.com/xenoviz/ruk/internal/lock"
)

type processProbe struct {
	result lockpkg.ProcessState
	err    error
	calls  int
}

func (probe *processProbe) Inspect(context.Context, int) (lockpkg.ProcessState, error) {
	probe.calls++
	return probe.result, probe.err
}

func TestDirectoryLockWritesOwnerAndRemovesOnlyItsToken(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	lockPath := filepath.Join(root, "state.lock")
	locker := newTestLocker(t, lockpkg.Config{
		PID:             41,
		Hostname:        "host-a",
		ProcessIdentity: "identity-41",
		Token:           func() string { return "owner-token" },
	})

	err := locker.With(context.Background(), lockPath, func() error {
		data, readErr := os.ReadFile(filepath.Join(lockPath, "owner.json"))
		if readErr != nil {
			t.Fatalf("read owner: %v", readErr)
		}
		var owner lockpkg.Owner
		if decodeErr := json.Unmarshal(data, &owner); decodeErr != nil {
			t.Fatalf("decode owner: %v", decodeErr)
		}
		if owner.PID != 41 || owner.Hostname != "host-a" || owner.Token != "owner-token" || owner.ProcessIdentity != "identity-41" {
			t.Fatalf("owner = %#v", owner)
		}
		if owner.CreatedAt != "2026-01-02T03:04:05.000Z" {
			t.Fatalf("createdAt = %q", owner.CreatedAt)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("With returned an error: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock still exists after release: %v", err)
	}

	guard, err := locker.Acquire(context.Background(), lockPath)
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	writeOwner(t, lockPath, lockpkg.Owner{PID: 99, Hostname: "host-a", Token: "replacement-token", CreatedAt: "2026-01-02T03:04:05.000Z"})
	if err := guard.Release(); err != nil {
		t.Fatalf("Release returned an error: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("replacement lock was removed: %v", err)
	}
}

func TestDirectoryLockNeverStealsLiveLocalOwnerByAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	root := t.TempDir()
	lockPath := filepath.Join(root, "state.lock")
	writeOwner(t, lockPath, lockpkg.Owner{
		PID:             72,
		Hostname:        "host-a",
		Token:           "live-token",
		CreatedAt:       now.Add(-time.Hour).Format(time.RFC3339Nano),
		ProcessIdentity: "identity-72",
	})
	setModified(t, lockPath, now.Add(-time.Hour))
	probe := &processProbe{result: lockpkg.ProcessState{Alive: true, IdentityKnown: true, Identity: "identity-72"}}
	locker := newTestLocker(t, lockpkg.Config{
		Now:      func() time.Time { return now },
		PID:      41,
		Hostname: "host-a",
		Token:    func() string { return "contender-token" },
		Probe:    probe,
		Options: lockpkg.Options{
			Timeout: 2 * time.Millisecond,
			Stale:   10 * time.Millisecond,
		},
		Sleep: func(context.Context, time.Duration) error {
			now = now.Add(2 * time.Millisecond)
			return nil
		},
	})

	_, err := locker.Acquire(context.Background(), lockPath)
	var timeout *lockpkg.TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Acquire error = %v, want TimeoutError", err)
	}
	if probe.calls == 0 {
		t.Fatal("live owner was not inspected")
	}
	data, err := os.ReadFile(filepath.Join(lockPath, "owner.json"))
	if err != nil || !strings.Contains(string(data), "live-token") {
		t.Fatalf("live owner changed: %q, %v", data, err)
	}
}

func TestDirectoryLockTreatsUnknownLocalIdentityAsLive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	lockPath := filepath.Join(t.TempDir(), "state.lock")
	writeOwner(t, lockPath, lockpkg.Owner{
		PID:             72,
		Hostname:        "host-a",
		Token:           "unknown-token",
		CreatedAt:       now.Add(-time.Hour).Format(time.RFC3339Nano),
		ProcessIdentity: "identity-72",
	})
	setModified(t, lockPath, now.Add(-time.Hour))
	locker := newTestLocker(t, lockpkg.Config{
		Now:      func() time.Time { return now },
		PID:      41,
		Hostname: "host-a",
		Token:    func() string { return "contender-token" },
		Probe: &processProbe{
			result: lockpkg.ProcessState{Alive: true, IdentityKnown: false},
		},
		Options: lockpkg.Options{Timeout: time.Millisecond, Stale: time.Millisecond},
		Sleep: func(context.Context, time.Duration) error {
			now = now.Add(time.Millisecond)
			return nil
		},
	})

	_, err := locker.Acquire(context.Background(), lockPath)
	var timeout *lockpkg.TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Acquire error = %v, want TimeoutError", err)
	}
}

func TestDirectoryLockRecoversDeadOwnerByAtomicRename(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	root := t.TempDir()
	lockPath := filepath.Join(root, "state.lock")
	writeOwner(t, lockPath, lockpkg.Owner{
		PID:             72,
		Hostname:        "host-a",
		Token:           "dead-token",
		CreatedAt:       now.Add(-time.Hour).Format(time.RFC3339Nano),
		ProcessIdentity: "old-identity",
	})
	setModified(t, lockPath, now.Add(-time.Hour))
	locker := newTestLocker(t, lockpkg.Config{
		Now:      func() time.Time { return now },
		PID:      41,
		Hostname: "host-a",
		Token:    func() string { return "new-token" },
		Probe: &processProbe{
			result: lockpkg.ProcessState{Alive: true, IdentityKnown: true, Identity: "reused-identity"},
		},
		Options: lockpkg.Options{Timeout: time.Second, Stale: time.Minute},
	})

	guard, err := locker.Acquire(context.Background(), lockPath)
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	var tombstone string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "state.lock.abandoned-") {
			tombstone = entry.Name()
		}
	}
	if tombstone == "" {
		t.Fatalf("entries = %v, want abandoned tombstone", entries)
	}
	data, err := os.ReadFile(filepath.Join(lockPath, "owner.json"))
	if err != nil || !strings.Contains(string(data), "new-token") {
		t.Fatalf("new owner = %q, %v", data, err)
	}
	if err := guard.Release(); err != nil {
		t.Fatalf("Release returned an error: %v", err)
	}
}

func TestDirectoryLockGivesIncompleteOwnerOneSecondGrace(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	root := t.TempDir()
	lockPath := filepath.Join(root, "state.lock")
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatalf("create incomplete lock: %v", err)
	}
	setModified(t, lockPath, now.Add(-500*time.Millisecond))
	locker := newTestLocker(t, lockpkg.Config{
		Now:      func() time.Time { return now },
		PID:      41,
		Hostname: "host-a",
		Token:    func() string { return "new-token" },
		Options:  lockpkg.Options{Timeout: time.Millisecond, Stale: time.Millisecond},
		Sleep: func(context.Context, time.Duration) error {
			now = now.Add(time.Millisecond)
			return nil
		},
	})

	_, err := locker.Acquire(context.Background(), lockPath)
	var timeout *lockpkg.TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Acquire error = %v, want TimeoutError", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.lock" {
		t.Fatalf("entries = %v, incomplete lock was stolen", entries)
	}
}

func TestDirectoryLockHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "state.lock")
	writeOwner(t, lockPath, lockpkg.Owner{PID: 72, Hostname: "host-a", Token: "live-token", CreatedAt: "2026-01-02T03:04:05.000Z"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	locker := newTestLocker(t, lockpkg.Config{PID: 41, Hostname: "host-a"})

	_, err := locker.Acquire(ctx, lockPath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire error = %v, want context.Canceled", err)
	}
}

func newTestLocker(t *testing.T, config lockpkg.Config) *lockpkg.DirectoryLocker {
	t.Helper()
	if config.Now == nil {
		config.Now = func() time.Time {
			return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		}
	}
	if config.Sleep == nil {
		config.Sleep = func(context.Context, time.Duration) error { return nil }
	}
	if config.Token == nil {
		config.Token = func() string { return "test-token" }
	}
	return lockpkg.NewDirectoryLocker(config)
}

func writeOwner(t *testing.T, lockPath string, owner lockpkg.Owner) {
	t.Helper()
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatalf("create lock: %v", err)
	}
	data, err := json.Marshal(owner)
	if err != nil {
		t.Fatalf("encode owner: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, "owner.json"), data, 0o600); err != nil {
		t.Fatalf("write owner: %v", err)
	}
}

func setModified(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set modified time: %v", err)
	}
}
