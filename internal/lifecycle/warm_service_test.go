package lifecycle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

type warmStateReaderFake struct {
	state *state.State
	read  func()
}

func (reader warmStateReaderFake) Read(context.Context) (*state.State, error) {
	if reader.read != nil {
		reader.read()
	}
	return reader.state, nil
}

type warmLockerFake struct {
	paths []string
	enter func(string)
}

func (locker *warmLockerFake) With(_ context.Context, path string, callback func() error) error {
	locker.paths = append(locker.paths, path)
	if locker.enter != nil {
		locker.enter(path)
	}
	return callback()
}

type warmWorktreeFake struct {
	created     []string
	locked      []string
	createError error
	lockError   error
}

func (worktree *warmWorktreeFake) Create(_ context.Context, path, branch, startPoint string, detach bool) error {
	if worktree.createError != nil {
		return worktree.createError
	}
	if branch != "(warm)" || startPoint == "" || !detach {
		return errors.New("invalid warm create request")
	}
	worktree.created = append(worktree.created, path)
	return nil
}

func (worktree *warmWorktreeFake) Lock(_ context.Context, path string) error {
	if worktree.lockError != nil {
		return worktree.lockError
	}
	worktree.locked = append(worktree.locked, path)
	return nil
}

func warmLifecycleService(store *memoryStore, ids []string) *lifecycle.Service {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return lifecycle.New(store, lifecycle.Options{
		Now: func() time.Time { return now },
		NewID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
}

func newWarmService(t *testing.T, store *memoryStore, worktree *warmWorktreeFake, locker *warmLockerFake, reader *warmStateReaderFake, validate lifecycle.WarmDependencyValidator, prepare lifecycle.WarmDependencyPreparer, paths ...string) *lifecycle.WarmService {
	t.Helper()
	ids := []string{preparationID, "unused-preparation-2", "unused-preparation-3"}
	lifecycleService := warmLifecycleService(store, ids)
	if len(paths) < 2 {
		paths = []string{"pool-maintenance.lock", "warm.lock"}
	}
	headReader := func(context.Context) (map[string]string, error) {
		heads := make(map[string]string)
		for _, workspace := range reader.state.Workspaces {
			heads[workspace.Path] = "head"
		}
		return heads, nil
	}
	return lifecycle.NewWarmService(lifecycle.WarmOptions{
		Lifecycle:               lifecycleService,
		Reader:                  reader,
		Locker:                  locker,
		Worktree:                worktree,
		PoolMaintenanceLockPath: paths[0],
		WarmLockPath:            paths[1],
		WorktreeHeads:           headReader,
		TargetHead:              func(context.Context, string) (string, error) { return "head", nil },
		ValidateDependencies:    validate,
		Prepare:                 prepare,
		WorkspacePath:           func(context.Context, int) (string, error) { return filepath.Join(t.TempDir(), "new-slot"), nil },
	})
}

func TestWarmCountsOnlyValidCapacityAndCreatesMissingAvailableSlot(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	workspacePath := filepath.Join(t.TempDir(), "existing")
	if err := os.MkdirAll(filepath.Join(workspacePath, "node_modules"), 0o700); err != nil {
		t.Fatalf("create projection: %v", err)
	}
	projection, err := dependencies.ProjectionFingerprint(workspacePath, []string{"node_modules"})
	if err != nil {
		t.Fatalf("ProjectionFingerprint returned an error: %v", err)
	}
	key, _ := state.TreeKey(workspacePath)
	available := "2026-01-01T00:00:00.000Z"
	store.current.Workspaces[key] = state.WorkspaceRecord{Path: workspacePath, Managed: true, Lifecycle: state.LifecycleAvailable, UpdatedAt: available, AvailableAt: &available}
	store.current.Trees[key] = state.TreeRecord{Path: workspacePath, Fingerprint: "same", ProjectionFingerprint: projection, Projections: []string{"node_modules"}}
	worktree := &warmWorktreeFake{}
	locker := &warmLockerFake{}
	reader := &warmStateReaderFake{state: store.current}
	service := newWarmService(t, store, worktree, locker, reader,
		func(context.Context, string, state.TreeRecord) (bool, error) { return true, nil },
		func(context.Context, string) (dependencies.EnsureResult, error) {
			return dependencies.EnsureResult{}, nil
		})
	result, err := service.Warm(context.Background(), lifecycle.WarmInput{Count: 2, StartPoint: "HEAD"})
	if err != nil {
		t.Fatalf("Warm returned an error: %v", err)
	}
	if result.Status != "warmed" || result.Requested != 2 || result.Available != 2 || len(result.Created) != 1 {
		t.Fatalf("warm result = %#v", result)
	}
	if len(worktree.created) != 1 || len(worktree.locked) != 1 || locker.paths[0] != "pool-maintenance.lock" || locker.paths[1] != "warm.lock" {
		t.Fatalf("warm operations = %#v / %#v", worktree, locker.paths)
	}
	createdKey, _ := state.TreeKey(result.Created[0])
	if store.current.Workspaces[createdKey].Lifecycle != state.LifecycleAvailable || store.current.Workspaces[createdKey].Assignment != nil {
		t.Fatalf("created workspace = %#v", store.current.Workspaces[createdKey])
	}
}

func TestWarmSkipsInvalidHeadOrProjectionAndRecordsFailure(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	path := filepath.Join(t.TempDir(), "stale")
	key, _ := state.TreeKey(path)
	available := "2026-01-01T00:00:00.000Z"
	store.current.Workspaces[key] = state.WorkspaceRecord{Path: path, Managed: true, Lifecycle: state.LifecycleAvailable, UpdatedAt: available, AvailableAt: &available}
	worktree := &warmWorktreeFake{createError: errors.New("Git create failed")}
	locker := &warmLockerFake{}
	reader := &warmStateReaderFake{state: store.current}
	service := newWarmService(t, store, worktree, locker, reader,
		func(context.Context, string, state.TreeRecord) (bool, error) { return true, nil },
		func(context.Context, string) (dependencies.EnsureResult, error) {
			return dependencies.EnsureResult{}, nil
		})
	result, err := service.Warm(context.Background(), lifecycle.WarmInput{Count: 1, StartPoint: "HEAD"})
	if err == nil || result.Status != "" {
		t.Fatalf("Warm result/error = %#v / %v", result, err)
	}
	if len(worktree.created) != 0 {
		t.Fatalf("failed Git create was recorded as successful: %#v", worktree)
	}
}

func TestWarmRejectsNonPositiveCountBeforeLocks(t *testing.T) {
	t.Parallel()
	store := newMemoryStore()
	locker := &warmLockerFake{}
	service := newWarmService(t, store, &warmWorktreeFake{}, locker, &warmStateReaderFake{state: store.current},
		func(context.Context, string, state.TreeRecord) (bool, error) { return true, nil },
		func(context.Context, string) (dependencies.EnsureResult, error) {
			return dependencies.EnsureResult{}, nil
		})
	if _, err := service.Warm(context.Background(), lifecycle.WarmInput{Count: 0, StartPoint: "HEAD"}); err == nil {
		t.Fatal("Warm accepted a non-positive count")
	}
	if len(locker.paths) != 0 {
		t.Fatalf("invalid count acquired locks: %v", locker.paths)
	}
}
