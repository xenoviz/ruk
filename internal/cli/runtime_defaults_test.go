package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type runtimeDefaultsWarmWorktree struct {
	created []string
	locked  []string
}

func (worktree *runtimeDefaultsWarmWorktree) Create(_ context.Context, path, branch, start string, detach bool) error {
	worktree.created = append(worktree.created, path+"|"+branch+"|"+start+"|"+boolText(detach))
	return nil
}

func (worktree *runtimeDefaultsWarmWorktree) Lock(_ context.Context, path string) error {
	worktree.locked = append(worktree.locked, path)
	return nil
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestNewRuntimeDefaultsProvidesApplicationRoutesAndNativeExecuteFallback(t *testing.T) {
	defaults, err := NewRuntimeDefaults(RuntimeDefaultsOptions{})
	if err != nil {
		t.Fatalf("NewRuntimeDefaults returned an error: %v", err)
	}
	options := defaults.Options()
	if options.Sync == nil || options.Create == nil || options.Acquire == nil || options.Release == nil || options.Remove == nil || options.Warm == nil || options.GC == nil || options.Run == nil || options.Exec == nil || options.Now == nil {
		t.Fatalf("runtime defaults options = %#v, want every production route", options)
	}
	if runner := processpkg.NewRunner(); runner.Spawner == nil || runner.Describer == nil || runner.Cleaner == nil {
		t.Fatal("native process runner is incomplete")
	}
}

func TestRuntimeWarmComposesLifecycleAndInjectedPreparationSeams(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	clock := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	worktree := &runtimeDefaultsWarmWorktree{}
	prepared := false
	defaults, err := NewRuntimeDefaults(RuntimeDefaultsOptions{
		Now:   func() time.Time { return clock },
		NewID: func() string { return "00000000-0000-4000-8000-000000000011" },
		Mutations: MutationAdapterOptions{
			Sync: func(_ context.Context, input SyncCommandInput) (SyncCommandResult, error) {
				prepared = input.Repository.Root != repository.Root
				return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
			},
		},
		WarmWorkspace:          func(git.Repository) (lifecycle.WarmWorkspaceService, error) { return worktree, nil },
		WarmHeads:              func(context.Context, git.Repository) (map[string]string, error) { return map[string]string{}, nil },
		WarmTargetHead:         func(context.Context, git.Repository, string, bool) (string, error) { return "target-commit", nil },
		WarmValidateDependency: func(context.Context, git.Repository, string, state.TreeRecord) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := defaults.Warm(context.Background(), repository, WarmRequest{Count: 1, From: "HEAD"})
	if err != nil {
		t.Fatalf("warm route returned an error: %v", err)
	}
	if result.Status != "warmed" || result.Requested != 1 || result.Available != 1 || len(result.Created) != 1 || !prepared || len(worktree.created) != 1 || len(worktree.locked) != 1 {
		t.Fatalf("warm result=%#v prepared=%v created=%#v locked=%#v", result, prepared, worktree.created, worktree.locked)
	}
}

func TestRuntimeGCPlanIsFailClosedAndUsesRepositoryState(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	clock := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	defaults, err := NewRuntimeDefaults(RuntimeDefaultsOptions{
		Now: func() time.Time { return clock }, NewID: func() string { return "00000000-0000-4000-8000-000000000012" },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := defaults.GC(context.Background(), repository, GCRequest{Options: lifecycle.GCOptions{
		OlderThan: clock.Add(-time.Hour), Now: clock, CurrentWorkspacePath: repository.Root,
	}})
	if err != nil {
		t.Fatalf("GC plan returned an error: %v", err)
	}
	if result.Status != "planned" || result.Removed == nil || result.Expired == nil {
		t.Fatalf("GC result = %#v, want canonical empty plan", result)
	}
}

func TestRuntimeRunExecutesAnUnmanagedWorkspaceAfterSynchronization(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	spawned := false
	prepared := false
	runner := processpkg.Runner{
		Spawner:   runtimeDefaultsSpawner{called: &spawned, child: &runtimeDefaultsChild{status: processpkg.ExitStatus{Code: 23}}},
		Describer: runtimeDefaultsDescriber{},
	}
	defaults, err := NewRuntimeDefaults(RuntimeDefaultsOptions{
		ExecuteRunner: runner,
		Mutations: MutationAdapterOptions{Sync: func(context.Context, SyncCommandInput) (SyncCommandResult, error) {
			prepared = true
			return SyncCommandResult{Status: "prepared"}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := defaults.Run(context.Background(), RunRouteInput{Repository: repository, CWD: repository.Root, Command: []string{"tool"}})
	if err != nil || code != 23 || !prepared || !spawned {
		t.Fatalf("run code=%d error=%v prepared=%v spawned=%v", code, err, prepared, spawned)
	}
}

type runtimeDefaultsSpawner struct {
	called *bool
	child  processpkg.Child
}

func (spawner runtimeDefaultsSpawner) Spawn(context.Context, processpkg.SpawnRequest) (processpkg.Child, error) {
	*spawner.called = true
	if spawner.child == nil {
		return nil, errors.New("unexpected process spawn")
	}
	return spawner.child, nil
}

type runtimeDefaultsChild struct{ status processpkg.ExitStatus }

func (*runtimeDefaultsChild) PID() int                          { return 42 }
func (child *runtimeDefaultsChild) Wait() processpkg.ExitStatus { return child.status }
func (*runtimeDefaultsChild) Signal(os.Signal) error            { return nil }

type runtimeDefaultsDescriber struct{}

func (runtimeDefaultsDescriber) Describe(context.Context, int, processpkg.ProcessMode, []string) (state.TrackedProcessRecord, error) {
	return state.TrackedProcessRecord{PID: 42, StartedAt: "identity-42"}, nil
}
