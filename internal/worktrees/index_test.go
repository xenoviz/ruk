package worktrees_test

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
	"github.com/xenoviz/ruk/internal/worktrees"
)

type mutexLocker struct {
	mutex sync.Mutex
}

func (locker *mutexLocker) With(ctx context.Context, _ string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	locker.mutex.Lock()
	defer locker.mutex.Unlock()
	return fn()
}

func fixedClock(value string) func() time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return parsed }
}

func newTestStore(t *testing.T, root string, exists func(string) bool, now func() time.Time) *worktrees.IndexStore {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod index root: %v", err)
	}
	store, err := worktrees.NewIndexStore(worktrees.IndexStoreOptions{
		Root: root, Locker: &mutexLocker{}, Now: now, Exists: exists,
	})
	if err != nil {
		t.Fatalf("NewIndexStore returned an error: %v", err)
	}
	return store
}

func TestDefaultIndexRootUsesHomeRukFolder(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	got, err := worktrees.DefaultIndexRoot()
	if err != nil {
		t.Fatalf("DefaultIndexRoot returned an error: %v", err)
	}
	want := filepath.Join(home, ".ruk")
	if got != want {
		t.Fatalf("DefaultIndexRoot = %q, want %q", got, want)
	}
}

func TestDefaultIndexRootFallsBackToUserConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX HOME/XDG_CONFIG_HOME fallback is the documented Unix path")
	}
	config := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", config)
	got, err := worktrees.DefaultIndexRoot()
	if err != nil {
		t.Fatalf("DefaultIndexRoot returned an error: %v", err)
	}
	want := filepath.Join(config, "ruk")
	if got != want {
		t.Fatalf("DefaultIndexRoot fallback = %q, want %q", got, want)
	}
}

func TestIndexStoreReadMissingReturnsCanonicalEmptyIndex(t *testing.T) {
	t.Parallel()

	index, err := newTestStore(t, "", nil, nil).Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if index.Version != worktrees.IndexVersion || index.Repositories == nil || len(index.Repositories) != 0 {
		t.Fatalf("empty index = %#v", index)
	}
}

func TestIndexStoreRecordsAndRereadsRepository(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	commonDir := filepath.Join(repoRoot, ".git")
	if err := os.Mkdir(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, "", nil, fixedClock("2026-08-19T10:00:00.000Z"))
	if err := store.RecordRepository(context.Background(), commonDir, repoRoot); err != nil {
		t.Fatalf("RecordRepository returned an error: %v", err)
	}

	index, err := store.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	key, err := state.TreeKey(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	record, exists := index.Repositories[key]
	if !exists {
		t.Fatalf("index does not contain %s: %#v", commonDir, index.Repositories)
	}
	if record.CommonDir != commonDir || record.Root != repoRoot || record.UpdatedAt != "2026-08-19T10:00:00.000Z" {
		t.Fatalf("unexpected record: %#v", record)
	}
}

func TestIndexStoreUpsertRefreshesRootAndUpdatedAt(t *testing.T) {
	t.Parallel()

	indexRoot := t.TempDir()
	firstRoot := t.TempDir()
	commonDir := filepath.Join(firstRoot, ".git")
	if err := os.Mkdir(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	first := newTestStore(t, indexRoot, func(string) bool { return true }, fixedClock("2026-08-19T10:00:00.000Z"))
	if err := first.RecordRepository(context.Background(), commonDir, firstRoot); err != nil {
		t.Fatal(err)
	}

	secondRoot := t.TempDir()
	second := newTestStore(t, indexRoot, func(string) bool { return true }, fixedClock("2026-08-19T11:30:00.000Z"))
	if err := second.RecordRepository(context.Background(), commonDir, secondRoot); err != nil {
		t.Fatal(err)
	}

	index, err := second.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key, err := state.TreeKey(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	record := index.Repositories[key]
	if record.Root != secondRoot || record.UpdatedAt != "2026-08-19T11:30:00.000Z" || record.CommonDir != commonDir {
		t.Fatalf("upsert did not refresh root and update time: %#v", record)
	}
	if len(index.Repositories) != 1 {
		t.Fatalf("upsert created extra records: %#v", index.Repositories)
	}
}

func TestIndexStorePruneRemovesMissingRepositoriesAndKeepsPresentOnes(t *testing.T) {
	t.Parallel()

	indexRoot := t.TempDir()
	keptRoot := t.TempDir()
	keptCommon := filepath.Join(keptRoot, ".git")
	removedRoot := t.TempDir()
	removedCommon := filepath.Join(removedRoot, ".git")
	for _, dir := range []string{keptCommon, removedCommon} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := newTestStore(t, indexRoot, func(string) bool { return true }, fixedClock("2026-08-19T10:00:00.000Z"))
	if err := store.RecordRepository(context.Background(), keptCommon, keptRoot); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRepository(context.Background(), removedCommon, removedRoot); err != nil {
		t.Fatal(err)
	}

	pruning := newTestStore(t, indexRoot, func(path string) bool { return path == keptCommon }, fixedClock("2026-08-19T12:00:00.000Z"))
	if err := pruning.RecordRepository(context.Background(), keptCommon, keptRoot); err != nil {
		t.Fatal(err)
	}
	index, err := pruning.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keptKey, err := state.TreeKey(keptCommon)
	if err != nil {
		t.Fatal(err)
	}
	removedKey, err := state.TreeKey(removedCommon)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := index.Repositories[removedKey]; exists {
		t.Fatalf("pruned repository is still present: %#v", index.Repositories)
	}
	if _, exists := index.Repositories[keptKey]; !exists {
		t.Fatalf("kept repository was pruned: %#v", index.Repositories)
	}
}

func TestDecodeIndexFailsClosed(t *testing.T) {
	t.Parallel()

	commonDir := filepath.Join(string(filepath.Separator), "repos", "app", ".git")
	root := filepath.Join(string(filepath.Separator), "repos", "app")
	key, err := state.TreeKey(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	valid := func() map[string]any {
		return map[string]any{
			"version": worktrees.IndexVersion,
			"repositories": map[string]any{
				key: map[string]any{
					"commonDir": commonDir,
					"root":      root,
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

	if _, err := worktrees.DecodeIndex(mutate(func(map[string]any) {}), "repositories.json"); err != nil {
		t.Fatalf("valid index failed to decode: %v", err)
	}
	cases := map[string][]byte{
		"not JSON":      []byte("{"),
		"wrong version": mutate(func(document map[string]any) { document["version"] = 2 }),
		"missing map":   mutate(func(document map[string]any) { delete(document, "repositories") }),
		"mismatched key": mutate(func(document map[string]any) {
			repos := document["repositories"].(map[string]any)
			repos["0123456789abcdef0123"] = repos[key]
			delete(repos, key)
		}),
		"relative common": mutate(func(document map[string]any) {
			record := document["repositories"].(map[string]any)[key].(map[string]any)
			record["commonDir"] = "relative/.git"
		}),
		"relative root": mutate(func(document map[string]any) {
			record := document["repositories"].(map[string]any)[key].(map[string]any)
			record["root"] = "relative"
		}),
		"bad timestamp": mutate(func(document map[string]any) {
			record := document["repositories"].(map[string]any)[key].(map[string]any)
			record["updatedAt"] = "2026-08-19T10:00:00Z"
		}),
	}
	for name, data := range cases {
		if _, err := worktrees.DecodeIndex(data, "repositories.json"); err == nil {
			t.Fatalf("%s index decoded without an error", name)
		}
	}
}

func TestIndexStoreUpdateFailsVisiblyOnInvalidIndex(t *testing.T) {
	t.Parallel()

	indexRoot := t.TempDir()
	invalid := filepath.Join(indexRoot, "repositories.json")
	if err := os.WriteFile(invalid, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, indexRoot, func(string) bool { return true }, nil)
	repoRoot := t.TempDir()
	commonDir := filepath.Join(repoRoot, ".git")
	if err := store.RecordRepository(context.Background(), commonDir, repoRoot); err == nil {
		t.Fatal("RecordRepository replaced an invalid index silently")
	}
}

func TestIndexStoreConcurrentRecordsKeepEveryRepository(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, "", func(string) bool { return true }, nil)
	base := t.TempDir()
	const count = 12
	var wait sync.WaitGroup
	failures := make(chan error, count)
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			repoRoot := filepath.Join(base, fmt.Sprintf("app-%d", index))
			commonDir := filepath.Join(repoRoot, ".git")
			if err := store.RecordRepository(context.Background(), commonDir, repoRoot); err != nil {
				failures <- err
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent RecordRepository returned an error: %v", err)
	}

	index, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Repositories) != count {
		t.Fatalf("index contains %d records, want %d", len(index.Repositories), count)
	}
}

func TestIndexStoreWritesOwnerOnlyIndexFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	repoRoot := t.TempDir()
	commonDir := filepath.Join(repoRoot, ".git")
	if err := os.Mkdir(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	indexRoot := t.TempDir()
	store := newTestStore(t, indexRoot, nil, nil)
	if err := store.RecordRepository(context.Background(), commonDir, repoRoot); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(indexRoot, "repositories.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("index mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestIndexStoreRejectsSymlinkedIndexFile(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink ownership checks are POSIX-specific in this package")
	}

	indexRoot := t.TempDir()
	target := filepath.Join(indexRoot, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"repositories":{}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(indexRoot, "repositories.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := newTestStore(t, indexRoot, nil, nil)
	_, err := store.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("Read error = %v, want unsafe symlink failure", err)
	}
}
