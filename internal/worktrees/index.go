package worktrees

// Package worktrees owns the host-level index of repositories that have a
// Ruk per-repo worktree registry. The index maps repositories to their
// registries and contains no worktree records of its own. Per-repository
// worktrees.json files remain the source of truth.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

// IndexVersion is the canonical host repository-index schema version.
const IndexVersion = 1

const (
	indexFileName = "repositories.json"
	indexLockName = "repositories.lock"
	indexTime     = "2006-01-02T15:04:05.000Z"
)

// RepositoryRecord names one Git repository that has a Ruk worktree registry.
type RepositoryRecord struct {
	CommonDir string `json:"commonDir"`
	Root      string `json:"root"`
	UpdatedAt string `json:"updatedAt"`
}

// Index is the canonical in-memory host repository index. Map keys are
// state.TreeKey(record.CommonDir).
type Index struct {
	Version      int                         `json:"version"`
	Repositories map[string]RepositoryRecord `json:"repositories"`
}

// Locker serializes one index mutation against other local Ruk operations.
type Locker interface {
	With(context.Context, string, func() error) error
}

// IndexStoreOptions supplies the host index location and seams.
type IndexStoreOptions struct {
	Root   string
	Locker Locker
	Now    func() time.Time
	Exists func(string) bool
}

// IndexStore reads and atomically updates the per-user host repository index.
type IndexStore struct {
	root     string
	file     string
	lockPath string
	locker   Locker
	now      func() time.Time
	exists   func(string) bool
}

// DefaultIndexRoot returns the stable per-user ~/.ruk folder, uniform across
// operating systems and deliberately avoiding os.TempDir. The UserConfigDir
// fallback only applies when the platform cannot report a home directory.
func DefaultIndexRoot() (string, error) {
	home, homeErr := os.UserHomeDir()
	if homeErr == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".ruk"), nil
	}
	config, configErr := os.UserConfigDir()
	if configErr != nil {
		if homeErr != nil {
			return "", fmt.Errorf("resolve per-user repository index root: %w", homeErr)
		}
		return "", fmt.Errorf("resolve per-user repository index root: %w", configErr)
	}
	return filepath.Join(config, "ruk"), nil
}

// EmptyIndex returns the canonical zero index.
func EmptyIndex() *Index {
	return &Index{Version: IndexVersion, Repositories: map[string]RepositoryRecord{}}
}

// NewIndexStore creates an IndexStore. An empty Root selects DefaultIndexRoot.
// A nil Now selects the wall clock. A nil Exists selects os.Stat.
func NewIndexStore(options IndexStoreOptions) (*IndexStore, error) {
	if options.Locker == nil {
		return nil, errors.New("worktrees: nil index locker")
	}
	root := strings.TrimSpace(options.Root)
	if root == "" {
		resolved, err := DefaultIndexRoot()
		if err != nil {
			return nil, err
		}
		root = resolved
	}
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolve host repository index root: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	exists := options.Exists
	if exists == nil {
		exists = pathExists
	}
	return &IndexStore{
		root:     absolute,
		file:     filepath.Join(absolute, indexFileName),
		lockPath: filepath.Join(absolute, indexLockName),
		locker:   options.Locker,
		now:      now,
		exists:   exists,
	}, nil
}

// DecodeIndex parses and validates one persisted host repository index.
// Invalid indexes fail visibly, exactly like the per-repo worktree registry.
func DecodeIndex(data []byte, source string) (*Index, error) {
	var persisted Index
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("Cannot parse Ruk host repository index in %s: %w", source, err)
	}
	if err := validateIndex(&persisted); err != nil {
		return nil, fmt.Errorf("Unsupported or invalid Ruk host repository index in %s: %w", strings.TrimSpace(source), err)
	}
	return &persisted, nil
}

func validateIndex(index *Index) error {
	if index == nil || index.Version != IndexVersion || index.Repositories == nil {
		return errors.New("index version or repositories map is invalid")
	}
	for key, record := range index.Repositories {
		if !filepath.IsAbs(record.CommonDir) || !filepath.IsAbs(record.Root) {
			return fmt.Errorf("repository paths for %q are not absolute", key)
		}
		derivedKey, err := state.TreeKey(record.CommonDir)
		if err != nil || key != derivedKey {
			return fmt.Errorf("repository key %q does not match its common directory", key)
		}
		if !validIndexTimestamp(record.UpdatedAt) {
			return fmt.Errorf("repository record for %s has an invalid timestamp", record.Root)
		}
	}
	return nil
}

// Read returns the latest valid index without taking the mutation lock.
func (store *IndexStore) Read(ctx context.Context) (*Index, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read host repository index: %w", err)
	}
	info, err := os.Lstat(store.file)
	if errors.Is(err, os.ErrNotExist) {
		return EmptyIndex(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect host repository index %s: %w", store.file, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe Ruk host repository index file %s", store.file)
	}
	if err := verifyIndexFileOwner(info, store.file); err != nil {
		return nil, fmt.Errorf("unsafe Ruk host repository index file %s: %w", store.file, err)
	}
	data, err := os.ReadFile(store.file)
	if err != nil {
		return nil, fmt.Errorf("read host repository index %s: %w", store.file, err)
	}
	return DecodeIndex(data, store.file)
}

// Update executes and persists one index mutation while holding the index lock.
// Stale records whose CommonDir no longer exists are pruned before mutate runs.
func (store *IndexStore) Update(ctx context.Context, mutate func(*Index) error) error {
	if mutate == nil {
		return errors.New("worktrees: nil index mutation")
	}
	return store.locker.With(ctx, store.lockPath, func() error {
		if err := ensureIndexRoot(store.root); err != nil {
			return err
		}
		current, err := store.Read(ctx)
		if err != nil {
			return err
		}
		for key, record := range current.Repositories {
			if !store.exists(record.CommonDir) {
				delete(current.Repositories, key)
			}
		}
		if err := mutate(current); err != nil {
			return err
		}
		encoded, err := encodeIndex(current, store.file)
		if err != nil {
			return err
		}
		return store.replace(encoded)
	})
}

// RecordRepository upserts the host index entry for one Git repository.
func (store *IndexStore) RecordRepository(ctx context.Context, commonDir, root string) error {
	if strings.TrimSpace(commonDir) == "" {
		return errors.New("Git common directory must not be empty")
	}
	if strings.TrimSpace(root) == "" {
		return errors.New("repository root must not be empty")
	}
	if !filepath.IsAbs(commonDir) || !filepath.IsAbs(root) {
		return errors.New("repository paths must be absolute")
	}
	commonDir = filepath.Clean(commonDir)
	root = filepath.Clean(root)
	key, err := state.TreeKey(commonDir)
	if err != nil {
		return err
	}
	timestamp := canonicalIndexTimestamp(store.now())
	return store.Update(ctx, func(index *Index) error {
		index.Repositories[key] = RepositoryRecord{CommonDir: commonDir, Root: root, UpdatedAt: timestamp}
		return nil
	})
}

func encodeIndex(index *Index, source string) ([]byte, error) {
	if err := validateIndex(index); err != nil {
		return nil, fmt.Errorf("Unsupported or invalid Ruk host repository index in %s: %w", strings.TrimSpace(source), err)
	}
	indented, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode host repository index %s: %w", source, err)
	}
	return append(indented, '\n'), nil
}

func (store *IndexStore) replace(encoded []byte) (result error) {
	temporary := fmt.Sprintf("%s.%d.tmp", store.file, os.Getpid())
	committed := false
	defer func() {
		if !committed {
			if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) && result == nil {
				result = fmt.Errorf("remove temporary host repository index %s: %w", temporary, err)
			}
		}
	}()

	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write temporary host repository index %s: %w", temporary, err)
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return fmt.Errorf("secure temporary host repository index %s: %w", temporary, err)
	}
	if err := replaceIndexFile(temporary, store.file); err != nil {
		return fmt.Errorf("replace host repository index %s: %w", store.file, err)
	}
	committed = true
	return nil
}

func ensureIndexRoot(root string) error {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return errors.New("host repository index root must be an absolute path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create host repository index directory %s: %w", root, err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect host repository index directory %s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("unsafe Ruk host repository index directory %s", root)
	}
	if err := verifyIndexRootOwner(info, root); err != nil {
		return fmt.Errorf("unsafe Ruk host repository index directory %s: %w", root, err)
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func canonicalIndexTimestamp(now time.Time) string {
	return now.UTC().Truncate(time.Millisecond).Format(indexTime)
}

func validIndexTimestamp(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return false
	}
	return parsed.UTC().Format(indexTime) == value
}
