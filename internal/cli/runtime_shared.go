package cli

import (
	"context"
	"fmt"
	"os"
	ossignal "os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

func runtimeState(ctx context.Context, repository git.Repository, now func() time.Time, newID func() string) (*state.Store, *lock.DirectoryLocker, *lifecycle.Service, error) {
	if err := validateRepositoryContext(repository); err != nil {
		return nil, nil, nil, err
	}
	locker, err := newNativeDirectoryLocker(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	store := state.NewStore(repository.CommonDir, locker)
	return store, locker, lifecycle.New(store, lifecycle.Options{Now: now, NewID: newID}), nil
}

func verifyRuntimeAssignment(ctx context.Context, store *state.Store, assignmentID, path string) error {
	snapshot, err := store.Read(ctx)
	if err != nil {
		return err
	}
	key, err := state.TreeKey(path)
	if err != nil {
		return err
	}
	workspace, ok := snapshot.Workspaces[key]
	if !ok || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID || workspace.Lifecycle != state.LifecycleAssigned || workspace.OperationID != nil {
		return fmt.Errorf("Assignment %s does not exist or no longer owns %s", assignmentID, path)
	}
	return nil
}

func runtimeManagedSignals() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 2)
	ossignal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals, func() {
		ossignal.Stop(signals)
		close(signals)
	}
}

func runtimeEnvironmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name != "" {
			result[name] = value
		}
	}
	return result
}
