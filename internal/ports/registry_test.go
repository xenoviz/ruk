package ports

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type registryTestLocker struct {
	mutex sync.Mutex
	calls int
}

func (locker *registryTestLocker) With(ctx context.Context, _ string, callback func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	locker.mutex.Lock()
	defer locker.mutex.Unlock()
	locker.calls++
	return callback()
}

type registryTestActivity struct {
	active map[string]bool
	err    error
}

func (activity registryTestActivity) Active(_ context.Context, _ string, assignment string, port int64) (bool, error) {
	if activity.err != nil {
		return false, activity.err
	}
	return activity.active[assignment+":"+strconvForTest(port)], nil
}

func strconvForTest(port int64) string {
	return strconv.FormatInt(port, 10)
}

func newTestRegistry(t *testing.T, activity ActivityChecker) (*Registry, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "host")
	locker := &registryTestLocker{}
	registry, err := NewRegistry(RegistryOptions{Root: root, Locker: locker, Activity: activity})
	if err != nil {
		t.Fatal(err)
	}
	return registry, root
}

func TestRegistryPrunesStaleReservationsAndEnforcesUniqueActivePorts(t *testing.T) {
	active := registryTestActivity{active: map[string]bool{"live:3100": true}}
	registry, root := newTestRegistry(t, active)
	statePath := filepath.Join(root, "state.json")
	err := registry.With(context.Background(), func(transaction *ReservationTransaction) error {
		if err := transaction.Reserve(3100, "live", statePath); err != nil {
			return err
		}
		return transaction.Reserve(3100, "other", statePath)
	})
	if err == nil || !strings.Contains(err.Error(), "already reserved") {
		t.Fatalf("duplicate active reservation error = %v", err)
	}

	data := []byte(`{"version":1,"ports":{"3100":{"assignmentId":"live","statePath":` + strconv.Quote(statePath) + `},"3101":{"assignmentId":"stale","statePath":` + strconv.Quote(statePath) + `}}}`)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, registryFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = registry.With(context.Background(), func(transaction *ReservationTransaction) error {
		if _, ok := transaction.Reserved()[3100]; !ok {
			t.Fatal("live reservation was pruned")
		}
		if _, ok := transaction.Reserved()[3101]; ok {
			t.Fatal("stale reservation was retained")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRejectsCorruptionTable(t *testing.T) {
	registry, root := newTestRegistry(t, registryTestActivity{active: map[string]bool{}})
	statePath := filepath.Join(root, "state.json")
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "malformed", data: "{", want: "Cannot parse"},
		{name: "unsupported version", data: `{"version":2,"ports":{}}`, want: "Unsupported or invalid"},
		{name: "missing ports", data: `{"version":1}`, want: "Unsupported or invalid"},
		{name: "bad port key", data: `{"version":1,"ports":{"0001":{"assignmentId":"a","statePath":` + strconv.Quote(statePath) + `}}}`, want: "Unsupported or invalid"},
		{name: "unknown field", data: `{"version":1,"ports":{},"extra":true}`, want: "Cannot parse"},
		{name: "duplicate field", data: `{"version":1,"version":1,"ports":{}}`, want: "Cannot parse"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, registryFileName), []byte(testCase.data), 0o600); err != nil {
				t.Fatal(err)
			}
			err := registry.With(context.Background(), func(*ReservationTransaction) error { return nil })
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestRegistryCommitAndReleaseAreFenced(t *testing.T) {
	registry, root := newTestRegistry(t, registryTestActivity{active: map[string]bool{"assignment:3100": true}})
	statePath := filepath.Join(root, "state.json")
	var transaction *ReservationTransaction
	err := registry.With(context.Background(), func(current *ReservationTransaction) error {
		transaction = current
		if err := current.Reserve(3100, "assignment", statePath); err != nil {
			return err
		}
		return current.Commit()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err == nil {
		t.Fatal("second commit succeeded")
	}
	if err := transaction.Release("assignment"); err == nil {
		t.Fatal("release after commit succeeded")
	}
	if err := registry.Release(context.Background(), "assignment"); err != nil {
		t.Fatal(err)
	}
	decoded, err := os.ReadFile(filepath.Join(root, registryFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decoded), "assignment") {
		t.Fatalf("fenced release retained assignment: %s", decoded)
	}
}

func TestRegistryFailedActivityDoesNotRewriteCorruptibleRegistry(t *testing.T) {
	registry, root := newTestRegistry(t, registryTestActivity{err: errors.New("state unavailable")})
	statePath := filepath.Join(root, "state.json")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":1,"ports":{"3100":{"assignmentId":"a","statePath":` + strconv.Quote(statePath) + `}}}`)
	if err := os.WriteFile(filepath.Join(root, registryFileName), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.With(context.Background(), func(*ReservationTransaction) error { return nil }); err == nil {
		t.Fatal("activity failure was ignored")
	}
	got, err := os.ReadFile(filepath.Join(root, registryFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("registry changed after activity failure")
	}
}

func TestRegistryImportsActiveLegacyReservationsWithoutDeletingLegacyState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "go-host")
	legacyRoot := filepath.Join(t.TempDir(), "legacy-host")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(legacyRoot, "state.json")
	original := []byte(`{"version":1,"ports":{"3100":{"assignmentId":"legacy-assignment","statePath":` + strconv.Quote(statePath) + `}}}`)
	if err := os.WriteFile(filepath.Join(legacyRoot, registryFileName), original, 0o600); err != nil {
		t.Fatal(err)
	}
	activity := registryTestActivity{active: map[string]bool{"legacy-assignment:3100": true}}
	locker := &migrationTestLocker{}
	registry, err := NewRegistry(RegistryOptions{Root: root, LegacyRoot: legacyRoot, Locker: locker, Activity: activity})
	if err != nil {
		t.Fatal(err)
	}
	wantLocks := []string{filepath.Join(root, registryLockName), filepath.Join(legacyRoot, registryLockName)}
	err = registry.With(context.Background(), func(transaction *ReservationTransaction) error {
		if _, ok := transaction.Reserved()[3100]; !ok {
			t.Fatal("active legacy reservation was not imported")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(locker.paths, wantLocks) {
		t.Fatalf("migration lock order = %#v, want %#v", locker.paths, wantLocks)
	}
	legacy, err := os.ReadFile(filepath.Join(legacyRoot, registryFileName))
	if err != nil || string(legacy) != string(original) {
		t.Fatalf("legacy registry changed: %q, %v", legacy, err)
	}
	current, err := os.ReadFile(filepath.Join(root, registryFileName))
	if err != nil || !strings.Contains(string(current), "legacy-assignment") {
		t.Fatalf("Go registry did not retain imported reservation: %q, %v", current, err)
	}
}

func TestRegistryCreatesAndLocksAbsentLegacyRootBeforeReading(t *testing.T) {
	root := filepath.Join(t.TempDir(), "go-host")
	legacyRoot := filepath.Join(t.TempDir(), "legacy-host")
	locker := &migrationTestLocker{}
	registry, err := NewRegistry(RegistryOptions{
		Root: root, LegacyRoot: legacyRoot, Locker: locker,
		Activity: registryTestActivity{active: map[string]bool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.With(context.Background(), func(transaction *ReservationTransaction) error {
		if len(transaction.Reserved()) != 0 {
			t.Fatalf("absent legacy registry imported reservations = %#v", transaction.Reserved())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyRoot); err != nil {
		t.Fatalf("legacy root was not established before locking: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacyRoot, registryFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent legacy registry was written: %v", err)
	}
	wantLocks := []string{filepath.Join(root, registryLockName), filepath.Join(legacyRoot, registryLockName)}
	if !reflect.DeepEqual(locker.paths, wantLocks) {
		t.Fatalf("absent legacy lock order = %#v, want %#v", locker.paths, wantLocks)
	}
}

func TestRegistryRejectsConflictingActiveLegacyOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), "go-host")
	legacyRoot := filepath.Join(t.TempDir(), "legacy-host")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	goState := filepath.Join(root, "go-state.json")
	legacyState := filepath.Join(legacyRoot, "legacy-state.json")
	goData := []byte(`{"version":1,"ports":{"3100":{"assignmentId":"go-owner","statePath":` + strconv.Quote(goState) + `}}}`)
	legacyData := []byte(`{"version":1,"ports":{"3100":{"assignmentId":"legacy-owner","statePath":` + strconv.Quote(legacyState) + `}}}`)
	if err := os.WriteFile(filepath.Join(root, registryFileName), goData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, registryFileName), legacyData, 0o600); err != nil {
		t.Fatal(err)
	}
	activity := registryTestActivity{active: map[string]bool{"go-owner:3100": true, "legacy-owner:3100": true}}
	registry, err := NewRegistry(RegistryOptions{Root: root, LegacyRoot: legacyRoot, Locker: &migrationTestLocker{}, Activity: activity})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.With(context.Background(), func(*ReservationTransaction) error { return nil }); err == nil || !strings.Contains(err.Error(), "conflicting active") {
		t.Fatalf("conflicting active owners error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, registryFileName))
	if err != nil || string(got) != string(goData) {
		t.Fatalf("Go registry changed after conflict: %q, %v", got, err)
	}
}

func TestDefaultLegacyRegistryRootMatchesRukV02Location(t *testing.T) {
	got, err := DefaultLegacyRegistryRoot()
	if err != nil {
		t.Fatal(err)
	}
	var want string
	if runtime.GOOS == "windows" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			t.Fatal(homeErr)
		}
		want = filepath.Join(home, ".ruk-host")
	} else {
		want = filepath.Join("/tmp", "ruk-host-"+strconv.Itoa(os.Getuid()))
	}
	if got != want {
		t.Fatalf("legacy root = %q, want %q", got, want)
	}
}

func TestRegistryFailsClosedOnCorruptLegacyState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "go-host")
	legacyRoot := filepath.Join(t.TempDir(), "legacy-host")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacyRoot, registryFileName)
	if err := os.WriteFile(legacyFile, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(RegistryOptions{Root: root, LegacyRoot: legacyRoot, Locker: &migrationTestLocker{}, Activity: registryTestActivity{active: map[string]bool{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.With(context.Background(), func(*ReservationTransaction) error { return nil }); err == nil || !strings.Contains(err.Error(), "legacy port registry") {
		t.Fatalf("corrupt legacy registry error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, registryFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Go registry was written after legacy corruption: %v", err)
	}
}

type migrationTestLocker struct{ paths []string }

func (locker *migrationTestLocker) With(ctx context.Context, path string, callback func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	locker.paths = append(locker.paths, path)
	return callback()
}

func TestRegistryRejectsSymlinkRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated Windows privileges")
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(RegistryOptions{Root: root, Activity: registryTestActivity{active: map[string]bool{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.With(context.Background(), func(*ReservationTransaction) error { return nil }); err == nil || !strings.Contains(err.Error(), "Unsafe Ruk host port directory") {
		t.Fatalf("symlink root error = %v", err)
	}
}

func TestOSRegistryFileSystemRenameReplacesExistingDestination(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "ports.tmp")
	newPath := filepath.Join(root, registryFileName)
	if err := os.WriteFile(oldPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (osRegistryFileSystem{}).Rename(oldPath, newPath); err != nil {
		t.Fatalf("Rename returned an error: %v", err)
	}
	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("destination contents = %q, want %q", got, "new")
	}
}
