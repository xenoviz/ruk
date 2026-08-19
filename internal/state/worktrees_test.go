package state_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

func fixedWorktreeClock(value string) func() time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return parsed }
}

func TestWorktreeStoreReadMissingReturnsCanonicalEmptyRegistry(t *testing.T) {
	t.Parallel()

	store := state.NewWorktreeStore(t.TempDir(), &mutexLocker{}, nil)
	registry, err := store.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if registry.Version != state.WorktreeRegistryVersion {
		t.Fatalf("Version = %d, want %d", registry.Version, state.WorktreeRegistryVersion)
	}
	if registry.Worktrees == nil {
		t.Fatalf("Read returned a nil worktrees map: %#v", registry)
	}
}

func TestWorktreeStoreRecordsAndForgetsFolders(t *testing.T) {
	t.Parallel()

	commonDir := t.TempDir()
	store := state.NewWorktreeStore(commonDir, &mutexLocker{}, fixedWorktreeClock("2026-08-19T10:00:00.000Z"))
	workspace := filepath.Join(commonDir, "repo-ruk-agent-task-1234abcd")
	if err := store.RecordWorktree(context.Background(), workspace, "agent/task", state.WorktreeSourceAcquire); err != nil {
		t.Fatalf("RecordWorktree returned an error: %v", err)
	}

	registry, err := store.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	key, err := state.TreeKey(workspace)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	record, exists := registry.Worktrees[key]
	if !exists {
		t.Fatalf("registry does not contain %s: %#v", workspace, registry.Worktrees)
	}
	if record.Path != workspace || record.Branch != "agent/task" || record.Source != state.WorktreeSourceAcquire {
		t.Fatalf("unexpected record: %#v", record)
	}
	if record.CreatedAt != "2026-08-19T10:00:00.000Z" || record.UpdatedAt != "2026-08-19T10:00:00.000Z" {
		t.Fatalf("unexpected timestamps: %#v", record)
	}

	if err := store.ForgetWorktree(context.Background(), workspace); err != nil {
		t.Fatalf("ForgetWorktree returned an error: %v", err)
	}
	registry, err = store.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if len(registry.Worktrees) != 0 {
		t.Fatalf("registry still contains records: %#v", registry.Worktrees)
	}
	// Forgetting an unknown folder stays a safe idempotent no-op.
	if err := store.ForgetWorktree(context.Background(), workspace); err != nil {
		t.Fatalf("repeated ForgetWorktree returned an error: %v", err)
	}
}

func TestWorktreeStoreUpsertPreservesCreationProvenance(t *testing.T) {
	t.Parallel()

	commonDir := t.TempDir()
	workspace := filepath.Join(commonDir, "repo-ruk-warm-0-1234abcd")
	first := state.NewWorktreeStore(commonDir, &mutexLocker{}, fixedWorktreeClock("2026-08-19T10:00:00.000Z"))
	if err := first.RecordWorktree(context.Background(), workspace, "(warm)", state.WorktreeSourceWarm); err != nil {
		t.Fatalf("RecordWorktree returned an error: %v", err)
	}

	second := state.NewWorktreeStore(commonDir, &mutexLocker{}, fixedWorktreeClock("2026-08-19T11:30:00.000Z"))
	if err := second.RecordWorktree(context.Background(), workspace, "agent/reuse", state.WorktreeSourceAcquire); err != nil {
		t.Fatalf("upsert RecordWorktree returned an error: %v", err)
	}

	registry, err := second.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	key, err := state.TreeKey(workspace)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	record := registry.Worktrees[key]
	if record.Source != state.WorktreeSourceWarm || record.CreatedAt != "2026-08-19T10:00:00.000Z" {
		t.Fatalf("upsert rewrote provenance: %#v", record)
	}
	if record.Branch != "agent/reuse" || record.UpdatedAt != "2026-08-19T11:30:00.000Z" {
		t.Fatalf("upsert did not refresh branch and update time: %#v", record)
	}
}

func TestWorktreeStoreRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	store := state.NewWorktreeStore(t.TempDir(), &mutexLocker{}, nil)
	if err := store.RecordWorktree(context.Background(), "", "agent/task", state.WorktreeSourceCreate); err == nil {
		t.Fatal("RecordWorktree accepted an empty path")
	}
	if err := store.RecordWorktree(context.Background(), "/tmp/workspace", "", state.WorktreeSourceCreate); err == nil {
		t.Fatal("RecordWorktree accepted an empty branch")
	}
	if err := store.RecordWorktree(context.Background(), "/tmp/workspace", "agent/task", "adopted"); err == nil {
		t.Fatal("RecordWorktree accepted an unknown source")
	}
	if err := store.ForgetWorktree(context.Background(), ""); err == nil {
		t.Fatal("ForgetWorktree accepted an empty path")
	}
}

func TestWorktreeRegistryDecodeFailsClosed(t *testing.T) {
	t.Parallel()

	workspace := filepath.Join(string(filepath.Separator), "workspaces", "repo-agent-task")
	key, err := state.TreeKey(workspace)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	valid := func() map[string]any {
		return map[string]any{
			"version": state.WorktreeRegistryVersion,
			"worktrees": map[string]any{
				key: map[string]any{
					"path":      workspace,
					"branch":    "agent/task",
					"source":    state.WorktreeSourceCreate,
					"createdAt": "2026-08-19T10:00:00.000Z",
					"updatedAt": "2026-08-19T10:00:00.000Z",
				},
			},
		}
	}
	mutate := func(change func(map[string]any)) []byte {
		document := valid()
		change(document)
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
		return encoded
	}

	if _, err := state.DecodeWorktreeRegistry(mutate(func(map[string]any) {}), "worktrees.json"); err != nil {
		t.Fatalf("valid registry failed to decode: %v", err)
	}
	cases := map[string][]byte{
		"not JSON":        []byte("{"),
		"wrong version":   mutate(func(document map[string]any) { document["version"] = 2 }),
		"missing map":     mutate(func(document map[string]any) { delete(document, "worktrees") }),
		"mismatched key":  mutate(func(document map[string]any) { worktrees := document["worktrees"].(map[string]any); worktrees["0123456789abcdef0123"] = worktrees[key]; delete(worktrees, key) }),
		"relative path":   mutate(func(document map[string]any) { record := document["worktrees"].(map[string]any)[key].(map[string]any); record["path"] = "relative/path" }),
		"empty branch":    mutate(func(document map[string]any) { record := document["worktrees"].(map[string]any)[key].(map[string]any); record["branch"] = "" }),
		"unknown source":  mutate(func(document map[string]any) { record := document["worktrees"].(map[string]any)[key].(map[string]any); record["source"] = "adopted" }),
		"bad timestamp":   mutate(func(document map[string]any) { record := document["worktrees"].(map[string]any)[key].(map[string]any); record["createdAt"] = "yesterday" }),
		"bad update time": mutate(func(document map[string]any) { record := document["worktrees"].(map[string]any)[key].(map[string]any); record["updatedAt"] = "2026-08-19T10:00:00Z" }),
	}
	for name, data := range cases {
		if _, err := state.DecodeWorktreeRegistry(data, "worktrees.json"); err == nil {
			t.Fatalf("%s registry decoded without an error", name)
		}
	}
}

func TestWorktreeStoreReadFailsVisiblyOnInvalidRegistry(t *testing.T) {
	t.Parallel()

	commonDir := t.TempDir()
	paths := state.StorePaths(commonDir)
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		t.Fatalf("create registry directory: %v", err)
	}
	if err := os.WriteFile(paths.Worktrees, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatalf("write invalid registry: %v", err)
	}
	store := state.NewWorktreeStore(commonDir, &mutexLocker{}, nil)
	if _, err := store.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "Unsupported or invalid Ruk worktree registry") {
		t.Fatalf("Read did not fail visibly: %v", err)
	}
	if err := store.RecordWorktree(context.Background(), filepath.Join(commonDir, "workspace"), "agent/task", state.WorktreeSourceCreate); err == nil {
		t.Fatal("RecordWorktree replaced an invalid registry silently")
	}
}

func TestWorktreeStoreConcurrentRecordsKeepEveryFolder(t *testing.T) {
	t.Parallel()

	commonDir := t.TempDir()
	store := state.NewWorktreeStore(commonDir, &mutexLocker{}, nil)
	const worktreeCount = 12

	var wait sync.WaitGroup
	failures := make(chan error, worktreeCount)
	for index := range worktreeCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			workspace := filepath.Join(commonDir, fmt.Sprintf("workspace-%d", index))
			if err := store.RecordWorktree(context.Background(), workspace, fmt.Sprintf("agent/%d", index), state.WorktreeSourceAcquire); err != nil {
				failures <- err
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent RecordWorktree returned an error: %v", err)
	}

	registry, err := store.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if len(registry.Worktrees) != worktreeCount {
		t.Fatalf("registry contains %d records, want %d", len(registry.Worktrees), worktreeCount)
	}
}

func TestWorktreeStoreWritesOwnerOnlyRegistryFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	commonDir := t.TempDir()
	store := state.NewWorktreeStore(commonDir, &mutexLocker{}, nil)
	if err := store.RecordWorktree(context.Background(), filepath.Join(commonDir, "workspace"), "agent/task", state.WorktreeSourceCreate); err != nil {
		t.Fatalf("RecordWorktree returned an error: %v", err)
	}
	info, err := os.Stat(state.StorePaths(commonDir).Worktrees)
	if err != nil {
		t.Fatalf("stat registry file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode = %v, want 0600", info.Mode().Perm())
	}
}
