package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xenoviz/ruk/internal/atomicfile"
)

// Locker serializes one state mutation against other local Ruk operations.
type Locker interface {
	With(ctx context.Context, path string, fn func() error) error
}

// Paths names the durable state and lock locations below a Git common directory.
type Paths struct {
	Root          string
	Locks         string
	State         string
	StateLock     string
	Worktrees     string
	WorktreesLock string
}

// StorePaths returns the canonical repository-local state paths.
func StorePaths(commonDir string) Paths {
	root := filepath.Join(commonDir, "ruk")
	locks := filepath.Join(root, "locks")
	return Paths{
		Root:          root,
		Locks:         locks,
		State:         filepath.Join(root, "state.json"),
		StateLock:     filepath.Join(locks, "state.lock"),
		Worktrees:     filepath.Join(root, "worktrees.json"),
		WorktreesLock: filepath.Join(locks, "worktrees.lock"),
	}
}

// Store reads and atomically updates one repository's Ruk state.
type Store struct {
	paths  Paths
	locker Locker
}

// NewStore creates a Store rooted at a Git common directory.
func NewStore(commonDir string, locker Locker) *Store {
	if locker == nil {
		panic("state: nil locker")
	}
	return &Store{
		paths:  StorePaths(commonDir),
		locker: locker,
	}
}

// Read returns the latest valid state without taking the mutation lock.
func (store *Store) Read(ctx context.Context) (*State, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	data, err := os.ReadFile(store.paths.State)
	if errors.Is(err, os.ErrNotExist) {
		return emptyState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", store.paths.State, err)
	}
	decoded, err := Decode(data, store.paths.State)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

// Update executes and persists one state mutation while holding the state lock.
func (store *Store) Update(ctx context.Context, mutate func(*State) error) error {
	if mutate == nil {
		return errors.New("state: nil mutation")
	}
	return store.locker.With(ctx, store.paths.StateLock, func() error {
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
		encoded, err := encodeValidated(current, store.paths.State)
		if err != nil {
			return err
		}
		return store.replace(encoded)
	})
}

func emptyState() *State {
	return &State{
		Version:    CurrentVersion,
		Trees:      map[string]TreeRecord{},
		Workspaces: map[string]WorkspaceRecord{},
		Metrics:    EmptyMetrics(),
	}
}

func encodeValidated(current *State, source string) ([]byte, error) {
	if current == nil || current.Version != CurrentVersion || current.Trees == nil || current.Workspaces == nil {
		return nil, invalidState(source)
	}
	compact, err := json.Marshal(current)
	if err != nil {
		return nil, fmt.Errorf("encode state %s: %w", source, err)
	}
	if _, err := Decode(compact, source); err != nil {
		return nil, err
	}
	indented, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode state %s: %w", source, err)
	}
	return append(indented, '\n'), nil
}

func (store *Store) replace(encoded []byte) error {
	return atomicfile.Replace(store.paths.State, encoded, "state")
}
