package state

// This file owns the durable per-repository registry of worktrees created by
// Ruk. The registry lives beside state.json under the Git common directory,
// so linked worktrees share it, and it is keyed by worktree folder so one
// entry exists per created path. Like state.json, the registry is an
// optimization, not source of truth: Git remains authoritative for which
// worktrees exist, while the registry records which of them Ruk created.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorktreeRegistryVersion is the canonical worktree registry schema version.
const WorktreeRegistryVersion = 1

// Worktree sources name the Ruk operation that created a tracked worktree.
const (
	WorktreeSourceAcquire = "acquire"
	WorktreeSourceWarm    = "warm"
	WorktreeSourceCreate  = "create"
)

// WorktreeRecord describes one worktree folder created by Ruk.
type WorktreeRecord struct {
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	Source    string `json:"source"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// WorktreeRegistry is the canonical in-memory worktree registry. Map keys are
// TreeKey(record.Path), the same folder-derived key used by state records.
type WorktreeRegistry struct {
	Version   int                       `json:"version"`
	Worktrees map[string]WorktreeRecord `json:"worktrees"`
}

// EmptyWorktreeRegistry returns the canonical zero registry.
func EmptyWorktreeRegistry() *WorktreeRegistry {
	return &WorktreeRegistry{Version: WorktreeRegistryVersion, Worktrees: map[string]WorktreeRecord{}}
}

// ValidWorktreeSource reports whether source names a Ruk creation operation.
func ValidWorktreeSource(source string) bool {
	switch source {
	case WorktreeSourceAcquire, WorktreeSourceWarm, WorktreeSourceCreate:
		return true
	default:
		return false
	}
}

// DecodeWorktreeRegistry parses and validates one persisted worktree
// registry document. Invalid registries fail visibly, exactly like state.
func DecodeWorktreeRegistry(data []byte, source string) (*WorktreeRegistry, error) {
	var persisted WorktreeRegistry
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("Cannot parse Ruk worktree registry in %s: %w", source, err)
	}
	if err := validateWorktreeRegistry(&persisted); err != nil {
		return nil, fmt.Errorf("Unsupported or invalid Ruk worktree registry in %s: %w", strings.TrimSpace(source), err)
	}
	return &persisted, nil
}

func validateWorktreeRegistry(registry *WorktreeRegistry) error {
	if registry == nil || registry.Version != WorktreeRegistryVersion || registry.Worktrees == nil {
		return errors.New("registry version or worktrees map is invalid")
	}
	for key, record := range registry.Worktrees {
		if !filepath.IsAbs(record.Path) {
			return fmt.Errorf("worktree path %q is not absolute", record.Path)
		}
		derivedKey, err := TreeKey(record.Path)
		if err != nil || key != derivedKey {
			return fmt.Errorf("worktree key %q does not match its path", key)
		}
		if record.Branch == "" || !ValidWorktreeSource(record.Source) {
			return fmt.Errorf("worktree record for %s is incomplete", record.Path)
		}
		if !validTimestamp(record.CreatedAt) || !validTimestamp(record.UpdatedAt) {
			return fmt.Errorf("worktree record for %s has invalid timestamps", record.Path)
		}
	}
	return nil
}

// WorktreeStore reads and atomically updates one repository's registry of
// Ruk-created worktrees.
type WorktreeStore struct {
	paths  Paths
	locker Locker
	now    func() time.Time
}

// NewWorktreeStore creates a WorktreeStore rooted at a Git common directory.
// A nil now selects the wall clock.
func NewWorktreeStore(commonDir string, locker Locker, now func() time.Time) *WorktreeStore {
	if locker == nil {
		panic("state: nil worktree registry locker")
	}
	if now == nil {
		now = time.Now
	}
	return &WorktreeStore{paths: StorePaths(commonDir), locker: locker, now: now}
}

// Read returns the latest valid registry without taking the mutation lock.
func (store *WorktreeStore) Read(ctx context.Context) (*WorktreeRegistry, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read worktree registry: %w", err)
	}
	data, err := os.ReadFile(store.paths.Worktrees)
	if errors.Is(err, os.ErrNotExist) {
		return EmptyWorktreeRegistry(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read worktree registry %s: %w", store.paths.Worktrees, err)
	}
	return DecodeWorktreeRegistry(data, store.paths.Worktrees)
}

// Update executes and persists one registry mutation while holding the
// registry lock. The registry lock is separate from the state lock so
// tracking never contends with lifecycle transactions.
func (store *WorktreeStore) Update(ctx context.Context, mutate func(*WorktreeRegistry) error) error {
	if mutate == nil {
		return errors.New("state: nil worktree registry mutation")
	}
	return store.locker.With(ctx, store.paths.WorktreesLock, func() error {
		if err := os.MkdirAll(store.paths.Root, 0o700); err != nil {
			return fmt.Errorf("create state directory %s: %w", store.paths.Root, err)
		}
		current, err := store.Read(ctx)
		if err != nil {
			return err
		}
		if err := mutate(current); err != nil {
			return err
		}
		encoded, err := encodeWorktreeRegistry(current, store.paths.Worktrees)
		if err != nil {
			return err
		}
		return store.replace(encoded)
	})
}

// RecordWorktree upserts the registry entry for one Ruk-created worktree
// folder. An existing entry keeps its creation time and original source and
// refreshes only the branch and update time, so branch reassignment on a
// pooled worktree does not rewrite its provenance.
func (store *WorktreeStore) RecordWorktree(ctx context.Context, path, branch, source string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("worktree path must not be empty")
	}
	if strings.TrimSpace(branch) == "" {
		return errors.New("worktree branch must not be empty")
	}
	if !ValidWorktreeSource(source) {
		return fmt.Errorf("unknown worktree source %q", source)
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	key, err := TreeKey(absolute)
	if err != nil {
		return err
	}
	timestamp := canonicalWorktreeTimestamp(store.now())
	return store.Update(ctx, func(registry *WorktreeRegistry) error {
		record, exists := registry.Worktrees[key]
		if !exists {
			record = WorktreeRecord{Path: absolute, Source: source, CreatedAt: timestamp}
		}
		record.Branch = branch
		record.UpdatedAt = timestamp
		registry.Worktrees[key] = record
		return nil
	})
}

// ForgetWorktree removes the registry entry for one worktree folder. An
// unknown path is a safe no-op so removal retries stay idempotent.
func (store *WorktreeStore) ForgetWorktree(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("worktree path must not be empty")
	}
	key, err := TreeKey(path)
	if err != nil {
		return err
	}
	return store.Update(ctx, func(registry *WorktreeRegistry) error {
		delete(registry.Worktrees, key)
		return nil
	})
}

func encodeWorktreeRegistry(registry *WorktreeRegistry, source string) ([]byte, error) {
	if err := validateWorktreeRegistry(registry); err != nil {
		return nil, fmt.Errorf("Unsupported or invalid Ruk worktree registry in %s: %w", strings.TrimSpace(source), err)
	}
	indented, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode worktree registry %s: %w", source, err)
	}
	return append(indented, '\n'), nil
}

func (store *WorktreeStore) replace(encoded []byte) (result error) {
	temporary := fmt.Sprintf("%s.%d.tmp", store.paths.Worktrees, os.Getpid())
	committed := false
	defer func() {
		if !committed {
			if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) && result == nil {
				result = fmt.Errorf("remove temporary worktree registry %s: %w", temporary, err)
			}
		}
	}()

	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write temporary worktree registry %s: %w", temporary, err)
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return fmt.Errorf("secure temporary worktree registry %s: %w", temporary, err)
	}
	if err := replaceStateFile(temporary, store.paths.Worktrees); err != nil {
		return fmt.Errorf("replace worktree registry %s: %w", store.paths.Worktrees, err)
	}
	committed = true
	return nil
}

func canonicalWorktreeTimestamp(now time.Time) string {
	return now.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}
