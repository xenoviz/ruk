package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type runtimeDefaultsWarmWorktree struct {
	created []string
	locked  []string
}

type runtimeShellTerminalStub struct{ called bool }

func (stub *runtimeShellTerminalStub) Run(context.Context, ShellTerminalRequest) (ShellTerminalResult, error) {
	stub.called = true
	return ShellTerminalResult{DescendantsDrained: true}, nil
}

func TestRuntimeShellUsesConfiguredActivityRunnerAroundManagedShell(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	terminal := &runtimeShellTerminalStub{}
	activityCalled := false
	released := false
	mutations := MutationAdapters{
		Acquire: func(context.Context, git.Repository, AcquireInput) (AcquireResult, error) {
			return AcquireResult{AcquireRecord: AcquireRecord{
				Status: "assigned", AssignmentID: "assignment-shell", Path: filepath.Join(root, "workspace"),
				ExpiresAt: "2026-08-16T13:00:00.000Z", Ports: map[string]int64{},
			}}, nil
		},
		Release: func(context.Context, ReleaseInput) (ReleaseResult, error) {
			released = true
			return ReleaseResult{}, nil
		},
	}
	activity := func(ctx context.Context, assignmentID string, operation func(context.Context) error) error {
		activityCalled = assignmentID == "assignment-shell"
		return operation(ctx)
	}
	result, err := runtimeShell(context.Background(), ShellRouteInput{
		Repository: repository, Branch: "agent/shell", TTL: "30", Owner: "owner", Now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		Stderr: io.Discard,
	}, func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) }, func() string { return "unused" }, mutations, RuntimeDefaultsOptions{
		ShellTerminal: terminal, ExecuteActivity: activity,
	})
	if err != nil || !activityCalled || !terminal.called || !released || !result.Released {
		t.Fatalf("result/error/activity/terminal/release = %#v/%v/%v/%v/%v", result, err, activityCalled, terminal.called, released)
	}
}

func TestRuntimeShellFullyInjectedSeamsSkipNativeStateInitialization(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	terminal := &runtimeShellTerminalStub{}
	mutations := MutationAdapters{
		Acquire: func(context.Context, git.Repository, AcquireInput) (AcquireResult, error) {
			return AcquireResult{AcquireRecord: AcquireRecord{
				Status: "assigned", AssignmentID: "assignment-shell", Path: filepath.Join(root, "workspace"),
				ExpiresAt: "2026-08-16T13:00:00.000Z", Ports: map[string]int64{},
			}}, nil
		},
		Release: func(context.Context, ReleaseInput) (ReleaseResult, error) { return ReleaseResult{}, nil },
	}
	activity := func(_ context.Context, _ string, operation func(context.Context) error) error {
		// The injected runner owns cancellation policy; use a live context so the
		// test isolates native state initialization from shell execution.
		return operation(context.Background())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runtimeShell(ctx, ShellRouteInput{
		Repository: repository, Branch: "agent/shell", TTL: "30", Owner: "owner",
		Now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC), Stderr: io.Discard,
	}, func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) }, func() string { return "unused" }, mutations, RuntimeDefaultsOptions{
		ShellTerminal: terminal, ExecuteActivity: activity,
	})
	if err != nil || !terminal.called || !result.Released {
		t.Fatalf("fully injected shell result/error/terminal = %#v/%v/%v", result, err, terminal.called)
	}
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
	if options.Sync == nil || options.Create == nil || options.Acquire == nil || options.Release == nil || options.Remove == nil || options.Warm == nil || options.GC == nil || options.Run == nil || options.Exec == nil || options.Shell == nil || options.Now == nil {
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
	syncJSON := false
	defaults, err := NewRuntimeDefaults(RuntimeDefaultsOptions{
		Now:   func() time.Time { return clock },
		NewID: func() string { return "00000000-0000-4000-8000-000000000011" },
		Mutations: MutationAdapterOptions{
			Sync: func(_ context.Context, input SyncCommandInput) (SyncCommandResult, error) {
				prepared = input.Repository.Root != repository.Root
				syncJSON = input.JSON
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
	result, err := defaults.Warm(context.Background(), repository, WarmRequest{Count: 1, From: "HEAD", JSON: true})
	if err != nil {
		t.Fatalf("warm route returned an error: %v", err)
	}
	if result.Status != "warmed" || result.Requested != 1 || result.Available != 1 || len(result.Created) != 1 || !prepared || !syncJSON || len(worktree.created) != 1 || len(worktree.locked) != 1 {
		t.Fatalf("warm result=%#v prepared=%v syncJSON=%v created=%#v locked=%#v", result, prepared, syncJSON, worktree.created, worktree.locked)
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

func TestCanonicalRuntimePathResolvesMissingLeafThroughExistingAncestor(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "never-created", "workspace")

	got, err := canonicalRuntimePath(missing)
	if err != nil {
		t.Fatalf("canonicalRuntimePath returned an error for a missing leaf: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalRoot, "never-created", "workspace")
	if got != want {
		t.Fatalf("canonicalRuntimePath = %q, want %q", got, want)
	}
}

func TestCanonicalRuntimePathRejectsBlankInput(t *testing.T) {
	for _, value := range []string{"", " ", "\t"} {
		t.Run(value, func(t *testing.T) {
			if _, err := canonicalRuntimePath(value); err == nil {
				t.Fatal("canonicalRuntimePath accepted blank input")
			}
		})
	}
}

func TestCanonicalRuntimePathRejectsDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing-target"), dangling); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := canonicalRuntimePath(filepath.Join(dangling, "workspace"))
	if err == nil || !strings.Contains(err.Error(), "dangling symlink") {
		t.Fatalf("canonicalRuntimePath error = %v, want dangling symlink failure", err)
	}
}

func TestCanonicalRuntimePathResolvesExistingAncestorSymlinkForMissingLeaf(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	missing := filepath.Join(link, "never-created", "workspace")
	got, err := canonicalRuntimePath(missing)
	if err != nil {
		t.Fatalf("canonicalRuntimePath returned an error for a symlinked missing leaf: %v", err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalTarget, "never-created", "workspace")
	if got != want {
		t.Fatalf("canonicalRuntimePath = %q, want %q", got, want)
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

func TestRuntimeRunSubscribesAndForwardsManagedSignals(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	key, err := state.TreeKey(repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(repository.CommonDir, lock.NewDirectoryLocker(lock.Config{}))
	if err := store.Update(context.Background(), func(current *state.State) error {
		current.Workspaces[key] = state.WorkspaceRecord{
			Path: repository.Root, Managed: true, Branch: "agent/signals", Lifecycle: state.LifecycleAssigned,
			Assignment: &state.AssignmentRecord{
				ID: "33333333-3333-4333-8333-333333333333", Owner: "owner", Hostname: "host",
				AssignedAt: "2026-08-16T10:00:00.000Z", RenewedAt: "2026-08-16T10:00:00.000Z",
				ExpiresAt: "2026-08-16T18:00:00.000Z", LeaseDurationMinutes: 480,
				LastActivityAt: "2026-08-16T10:00:00.000Z", LeaseKeepers: []state.LeaseKeeperRecord{}, Ports: map[string]int64{},
			},
			Processes: []state.TrackedProcessRecord{}, CreatedAt: "2026-08-16T10:00:00.000Z", UpdatedAt: "2026-08-16T10:00:00.000Z",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	group := int64(42)
	forwarder := &runtimeDefaultsForwarder{}
	runner := processpkg.Runner{
		Spawner:   runtimeDefaultsSpawner{called: new(bool), child: &runtimeDefaultsChild{status: processpkg.ExitStatus{Code: 0}}},
		Describer: runtimeDefaultsDescriber{record: state.TrackedProcessRecord{PID: 42, GroupID: &group, StartedAt: "identity-42"}},
		Forwarder: forwarder,
	}
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	stopped := false
	defaults, err := NewRuntimeDefaults(RuntimeDefaultsOptions{
		ExecuteRunner: runner,
		ExecuteSignals: func() (<-chan os.Signal, func()) {
			return signals, func() { stopped = true }
		},
		ExecuteActivity: func(ctx context.Context, _ string, operation func(context.Context) error) error {
			return operation(ctx)
		},
		Mutations: MutationAdapterOptions{Sync: func(context.Context, SyncCommandInput) (SyncCommandResult, error) {
			return SyncCommandResult{Status: "prepared"}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := defaults.Run(context.Background(), RunRouteInput{Repository: repository, CWD: repository.Root, Command: []string{"tool"}})
	if err != nil || code != 0 || !stopped || len(forwarder.signals) != 1 || forwarder.signals[0] != os.Interrupt {
		t.Fatalf("run code=%d error=%v stopped=%v signals=%v", code, err, stopped, forwarder.signals)
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

type runtimeDefaultsDescriber struct{ record state.TrackedProcessRecord }

func (describer runtimeDefaultsDescriber) Describe(context.Context, int, processpkg.ProcessMode, []string) (state.TrackedProcessRecord, error) {
	if describer.record.PID != 0 {
		return describer.record, nil
	}
	return state.TrackedProcessRecord{PID: 42, StartedAt: "identity-42"}, nil
}

type runtimeDefaultsForwarder struct{ signals []os.Signal }

func (forwarder *runtimeDefaultsForwarder) Forward(_ context.Context, _ state.TrackedProcessRecord, signal os.Signal) error {
	forwarder.signals = append(forwarder.signals, signal)
	return nil
}
