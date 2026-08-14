package state_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xenoviz/ruk/internal/state"
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

func TestStoreReadMissingReturnsCanonicalEmptyState(t *testing.T) {
	t.Parallel()

	store := state.NewStore(t.TempDir(), &mutexLocker{})
	decoded, err := store.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if decoded.Version != state.CurrentVersion {
		t.Fatalf("Version = %d, want %d", decoded.Version, state.CurrentVersion)
	}
	if decoded.Trees == nil || decoded.Workspaces == nil {
		t.Fatalf("Read returned nil maps: %#v", decoded)
	}
}

func TestStoreUpdatesAreAtomicAcrossConcurrentWorkspaces(t *testing.T) {
	t.Parallel()

	commonDir := t.TempDir()
	store := state.NewStore(commonDir, &mutexLocker{})
	const workspaceCount = 12

	var wait sync.WaitGroup
	errors := make(chan error, workspaceCount)
	for index := range workspaceCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			workspace := filepath.Join(commonDir, fmt.Sprintf("workspace-%d", index))
			key, err := state.TreeKey(workspace)
			if err != nil {
				errors <- err
				return
			}
			err = store.Update(context.Background(), func(current *state.State) error {
				current.Trees[key] = state.TreeRecord{
					Path:        workspace,
					Fingerprint: fmt.Sprintf("fingerprint-%d", index),
					Mode:        "managed-install",
					Projections: []string{"node_modules"},
					Branch:      fmt.Sprintf("agent/%d", index),
					UpdatedAt:   "1970-01-01T00:00:00.000Z",
				}
				return nil
			})
			if err != nil {
				errors <- err
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent Update returned an error: %v", err)
	}

	decoded, err := store.Read(context.Background())
	if err != nil {
		t.Fatalf("Read returned an error: %v", err)
	}
	if len(decoded.Trees) != workspaceCount {
		t.Fatalf("Trees has %d records, want %d", len(decoded.Trees), workspaceCount)
	}

	paths := state.StorePaths(commonDir)
	persisted, err := os.ReadFile(paths.State)
	if err != nil {
		t.Fatalf("ReadFile returned an error: %v", err)
	}
	if len(persisted) == 0 || persisted[len(persisted)-1] != '\n' {
		t.Fatalf("persisted state does not end in a newline: %q", persisted)
	}
	if _, err := os.Stat(fmt.Sprintf("%s.%d.tmp", paths.State, os.Getpid())); !os.IsNotExist(err) {
		t.Fatalf("temporary state file remains after commit: %v", err)
	}
}
