package cli_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

func TestRemoveCommandRejectsCurrentAndManagedWorkspaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	managed := filepath.Join(filepath.Dir(root), "managed")
	key, err := state.TreeKey(managed)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{
		key: {Path: managed, Managed: true, Lifecycle: state.LifecycleAvailable},
	}, Metrics: state.EmptyMetrics()}
	removed := false
	service := cli.RemoveCommand{
		Canonicalize: func(string) (string, error) { return root, nil },
		ReadState:    func(context.Context, string) (state.State, error) { return snapshot, nil },
		Remove: func(context.Context, string, string, bool) error {
			removed = true
			return nil
		},
		DeleteTree: func(context.Context, string, string) error { return nil },
		WithLock:   func(_ context.Context, _ string, callback func() error) error { return callback() },
		LockPath:   func(string, string) string { return "lock" },
	}
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	if err := service.Run(context.Background(), cli.RemoveInput{Repository: repository, CWD: root, Path: "."}); err == nil || err.Error() != "Refusing to remove the current workspace" {
		t.Fatalf("current-workspace error = %v", err)
	}

	service.Canonicalize = func(value string) (string, error) {
		if !strings.Contains(value, "managed") {
			t.Fatalf("canonicalize input = %q", value)
		}
		return managed, nil
	}
	if err := service.Run(context.Background(), cli.RemoveInput{Repository: repository, CWD: root, Path: "../managed"}); err == nil || err.Error() != "Workspace belongs to the managed pool; use ruk gc --apply" {
		t.Fatalf("managed-workspace error = %v", err)
	}
	if removed {
		t.Fatal("Git removal ran for rejected workspace")
	}
}

func TestRemoveCommandReportsAssignedRecovery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	destination := filepath.Join(filepath.Dir(root), "assigned")
	key, _ := state.TreeKey(destination)
	service := cli.RemoveCommand{
		Canonicalize: func(string) (string, error) { return destination, nil },
		ReadState: func(context.Context, string) (state.State, error) {
			return state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{
				key: {Path: destination, Managed: true, Lifecycle: state.LifecycleAssigned, Assignment: &state.AssignmentRecord{ID: "assignment-1"}},
			}, Metrics: state.EmptyMetrics()}, nil
		},
	}
	err := service.Run(context.Background(), cli.RemoveInput{Repository: git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}, CWD: root, Path: destination})
	if err == nil || err.Error() != "Workspace is managed by assignment assignment-1; use ruk release assignment-1" {
		t.Fatalf("error = %v", err)
	}
}

func TestRemoveCommandSerializesGitAndTreeStateCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	destination := filepath.Join(filepath.Dir(root), "old")
	events := []string{}
	service := cli.RemoveCommand{
		Canonicalize: func(string) (string, error) { return destination, nil },
		ReadState: func(context.Context, string) (state.State, error) {
			return state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{}, Metrics: state.EmptyMetrics()}, nil
		},
		WithLock: func(_ context.Context, path string, callback func() error) error {
			events = append(events, "lock:"+path)
			return callback()
		},
		LockPath: func(commonDir, path string) string { return commonDir + ":" + path },
		Remove: func(_ context.Context, repositoryRoot, path string, force bool) error {
			if repositoryRoot != root || path != destination || !force {
				t.Fatalf("remove = %q %q %v", repositoryRoot, path, force)
			}
			events = append(events, "git")
			return nil
		},
		DeleteTree: func(_ context.Context, commonDir, path string) error {
			events = append(events, "state")
			return nil
		},
	}
	err := service.Run(context.Background(), cli.RemoveInput{
		Repository: git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}, CWD: root, Path: "../old", Force: true,
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if got := strings.Join(events, ","); !strings.HasSuffix(got, ",git,state") {
		t.Fatalf("events = %q", got)
	}
}

func TestRemoveCommandStopsTreeStateDeletionAfterGitFailure(t *testing.T) {
	want := errors.New("git remove failed")
	deleted := false
	service := cli.RemoveCommand{
		Canonicalize: func(string) (string, error) { return "/old", nil },
		ReadState: func(context.Context, string) (state.State, error) {
			return state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{}, Metrics: state.EmptyMetrics()}, nil
		},
		WithLock: func(_ context.Context, _ string, callback func() error) error { return callback() },
		LockPath: func(string, string) string { return "lock" },
		Remove:   func(context.Context, string, string, bool) error { return want },
		DeleteTree: func(context.Context, string, string) error {
			deleted = true
			return nil
		},
	}
	err := service.Run(context.Background(), cli.RemoveInput{Repository: git.Repository{Root: "/repo", CommonDir: "/repo/.git"}, CWD: "/repo", Path: "/old"})
	if !errors.Is(err, want) || deleted {
		t.Fatalf("error=%v deleted=%v", err, deleted)
	}
}
