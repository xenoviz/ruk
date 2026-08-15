package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type runtimeWorkspaceStub struct {
	created   []string
	assigned  []string
	returned  []string
	returnErr error
}

func TestDefaultReleaseProcessesUsesNativeIdentityFencedManager(t *testing.T) {
	if _, ok := defaultReleaseProcesses().(processpkg.NativeProcessManager); !ok {
		t.Fatalf("default release process manager = %T, want process.NativeProcessManager", defaultReleaseProcesses())
	}
}

func (stub *runtimeWorkspaceStub) Create(_ context.Context, path, branch, start string) error {
	stub.created = append(stub.created, strings.Join([]string{path, branch, start}, "|"))
	return nil
}

func (stub *runtimeWorkspaceStub) Assign(_ context.Context, path, branch, start string) error {
	stub.assigned = append(stub.assigned, strings.Join([]string{path, branch, start}, "|"))
	return nil
}

func (stub *runtimeWorkspaceStub) Return(_ context.Context, path string, force bool, projections []string) error {
	stub.returned = append(stub.returned, strings.Join([]string{path, forceString(force), strings.Join(projections, ",")}, "|"))
	return stub.returnErr
}

func forceString(force bool) string {
	if force {
		return "true"
	}
	return "false"
}

func TestMutationWorkspaceLockPathMatchesStateLockLayout(t *testing.T) {
	commonDir := filepath.Join(t.TempDir(), "common")
	workspace := filepath.Join(t.TempDir(), "workspace")
	path, err := MutationWorkspaceLockPath(commonDir, workspace)
	if err != nil {
		t.Fatalf("MutationWorkspaceLockPath returned an error: %v", err)
	}
	key, err := state.TreeKey(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(state.StorePaths(commonDir).Locks, "workspace-"+key+".lock")
	if path != want {
		t.Fatalf("lock path = %q, want %q", path, want)
	}
	if _, err := MutationWorkspaceLockPath(commonDir, ""); err == nil {
		t.Fatal("empty workspace path was accepted")
	}
}

func TestNewMutationAdaptersBuildsAllRoutesAndCreateOrchestratesFreshWorktree(t *testing.T) {
	workspace := &runtimeWorkspaceStub{}
	clock := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		Now:   func() time.Time { return clock },
		NewID: func() string { return "00000000-0000-4000-8000-000000000001" },
		Sync: func(_ context.Context, input SyncCommandInput) (SyncCommandResult, error) {
			if input.Repository.Root == "" {
				return SyncCommandResult{}, errors.New("missing sync root")
			}
			return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
		},
		CreateWorkspace: func(git.Repository) (CreateWorkspace, error) { return &createWorkspaceAdapterStub{}, nil },
		AcquireWorktree: func(git.Repository) (lifecycle.AcquisitionWorktree, error) { return workspace, nil },
		PortRegistry: func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
			return nil, nil, nil
		},
	})
	if err != nil {
		t.Fatalf("NewMutationAdapters returned an error: %v", err)
	}
	if adapters.Sync == nil || adapters.Create == nil || adapters.Acquire == nil || adapters.Release == nil || adapters.Remove == nil {
		t.Fatalf("adapters = %#v, want every route", adapters)
	}

	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	result, err := adapters.Acquire(context.Background(), repository, AcquireInput{
		Branch: "agent/fresh", Owner: "owner", Hostname: "host", JSON: true, Now: clock,
	})
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	if result.Path == "" || result.Status != "assigned" || len(workspace.created) != 1 || len(workspace.assigned) != 1 {
		t.Fatalf("result=%#v created=%#v assigned=%#v", result, workspace.created, workspace.assigned)
	}
	if !strings.Contains(workspace.created[0], "agent-fresh") || !strings.HasSuffix(workspace.created[0], "|HEAD") {
		t.Fatalf("created workspace = %q, want branch slug", workspace.created[0])
	}
}

func TestAcquirePreparationDoesNotReenterWorkspaceHandoffLock(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	workspace := &runtimeWorkspaceStub{}
	preparationCalled := false
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		Now:   func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
		NewID: func() string { return "00000000-0000-4000-8000-000000000020" },
		Sync: func(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
			if input.Ensure.Store == nil || input.Ensure.Locker == nil {
				return SyncCommandResult{}, errors.New("acquire preparation did not receive its lock seams")
			}
			lockPath, err := MutationWorkspaceLockPath(input.Repository.CommonDir, input.Repository.Root)
			if err != nil {
				return SyncCommandResult{}, err
			}
			if err := input.Ensure.Locker.With(ctx, lockPath, func() error {
				preparationCalled = true
				return nil
			}); err != nil {
				return SyncCommandResult{}, err
			}
			if err := input.Ensure.Store.Update(ctx, func(*state.State) error { return nil }); err != nil {
				return SyncCommandResult{}, err
			}
			return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
		},
		AcquireWorktree: func(git.Repository) (lifecycle.AcquisitionWorktree, error) { return workspace, nil },
		PortRegistry: func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
			return nil, nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapters.Acquire(context.Background(), repository, AcquireInput{
		Branch: "agent/no-lock-reentry", Owner: "owner", Hostname: "host",
		Now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	if !preparationCalled {
		t.Fatal("dependency preparation did not run while the acquisition handoff lock was held")
	}
}

func TestRandomMutationIDIsUUIDv4(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for index := 0; index < 8; index++ {
		if value := randomMutationID(); !pattern.MatchString(value) {
			t.Fatalf("randomMutationID() = %q, want UUID v4", value)
		}
	}
}

func TestAcquireResolvesStartPointForFreshAndReusedWorktrees(t *testing.T) {
	testCases := []struct {
		name   string
		seed   bool
		assert func(*testing.T, *runtimeWorkspaceStub)
	}{
		{name: "fresh", assert: func(t *testing.T, workspace *runtimeWorkspaceStub) {
			if len(workspace.created) != 1 || !strings.HasSuffix(workspace.created[0], "|resolved-start") {
				t.Fatalf("created=%#v, want resolved start point", workspace.created)
			}
		}},
		{name: "reused", seed: true, assert: func(t *testing.T, workspace *runtimeWorkspaceStub) {
			if len(workspace.assigned) != 1 || !strings.HasSuffix(workspace.assigned[0], "|resolved-start") {
				t.Fatalf("assigned=%#v, want resolved start point", workspace.assigned)
			}
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
			workspace := &runtimeWorkspaceStub{}
			if testCase.seed {
				seedAvailableWorkspace(t, repository.CommonDir, filepath.Join(root, "reusable"))
			}
			var requested string
			var fetched bool
			adapters, err := NewMutationAdapters(MutationAdapterOptions{
				NewID: func() string { return "00000000-0000-4000-8000-000000000002" },
				StartPointResolver: func(_ context.Context, _ git.Repository, value string, fetch bool) (string, error) {
					requested, fetched = value, fetch
					return "resolved-start", nil
				},
				Sync: func(_ context.Context, _ SyncCommandInput) (SyncCommandResult, error) {
					return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
				},
				AcquireWorktree: func(git.Repository) (lifecycle.AcquisitionWorktree, error) { return workspace, nil },
				PortRegistry: func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
					return nil, nil, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapters.Acquire(context.Background(), repository, AcquireInput{
				Branch: "agent/resolved", From: "origin/main", Fetch: true,
				Owner: "owner", Hostname: "host", Now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("Acquire returned an error: %v", err)
			}
			if requested != "origin/main" || !fetched {
				t.Fatalf("resolver inputs = (%q, %v), want (origin/main, true)", requested, fetched)
			}
			testCase.assert(t, workspace)
		})
	}
}

func TestAcquireStartPointResolutionFailureDoesNotMutateStateOrWorktree(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	workspaceCalled := false
	wantErr := errors.New("cannot resolve start point")
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) { return "", wantErr },
		AcquireWorktree: func(git.Repository) (lifecycle.AcquisitionWorktree, error) {
			workspaceCalled = true
			return &runtimeWorkspaceStub{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapters.Acquire(context.Background(), repository, AcquireInput{
		Branch: "agent/failure", Owner: "owner", Hostname: "host",
		Now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Acquire error = %v, want %v", err, wantErr)
	}
	if workspaceCalled {
		t.Fatal("worktree factory was called after start-point resolution failed")
	}
	if _, statErr := os.Stat(state.StorePaths(repository.CommonDir).State); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state mutation after resolution failure: stat error = %v", statErr)
	}
}

func TestAcquireRejectsInvalidCommonDirBeforeResolverOrStateConstruction(t *testing.T) {
	resolverCalled := false
	workspaceCalled := false
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) {
			resolverCalled = true
			return "HEAD", nil
		},
		AcquireWorktree: func(git.Repository) (lifecycle.AcquisitionWorktree, error) {
			workspaceCalled = true
			return &runtimeWorkspaceStub{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapters.Acquire(context.Background(), git.Repository{Root: "/repo", CommonDir: "relative-git-dir"}, AcquireInput{
		Branch: "agent/invalid-context", Owner: "owner", Hostname: "host",
	})
	if err == nil || !strings.Contains(err.Error(), "common directory") {
		t.Fatalf("Acquire error = %v, want common-directory validation error", err)
	}
	if resolverCalled || workspaceCalled {
		t.Fatalf("mutation seams called for invalid repository context: resolver=%v workspace=%v", resolverCalled, workspaceCalled)
	}
}

func TestAcquirePreparationFailureReturnsNativeWorktree(t *testing.T) {
	workspace := &runtimeWorkspaceStub{}
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		NewID: func() string { return "00000000-0000-4000-8000-000000000003" },
		Sync: func(context.Context, SyncCommandInput) (SyncCommandResult, error) {
			return SyncCommandResult{}, errors.New("dependency failure")
		},
		AcquireWorktree: func(git.Repository) (lifecycle.AcquisitionWorktree, error) { return workspace, nil },
		PortRegistry: func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
			return nil, nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapters.Acquire(context.Background(), repository, AcquireInput{
		Branch: "agent/cleanup", Owner: "owner", Hostname: "host",
		Now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	})
	if err == nil || len(workspace.returned) != 1 {
		t.Fatalf("Acquire error=%v returned=%#v, want failure with one return", err, workspace.returned)
	}
}

func TestProductionSyncDeniesSharedPrimaryCheckout(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{
		Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common"),
		PrimaryCheckout: true, PrimaryRoot: filepath.Join(root, "repo"),
	}
	seedAvailableWorkspace(t, repository.CommonDir, filepath.Join(root, "active"))
	locker := lock.NewDirectoryLocker(lock.Config{})
	store := state.NewStore(repository.CommonDir, locker)
	if err := store.Update(context.Background(), func(current *state.State) error {
		path := filepath.Join(root, "active-assignment")
		key, err := state.TreeKey(path)
		if err != nil {
			return err
		}
		now := "2026-08-16T12:00:00.000Z"
		expires := "2026-08-16T13:00:00.000Z"
		current.Workspaces[key] = state.WorkspaceRecord{
			Path: path, Managed: true, Branch: "agent/active", Lifecycle: state.LifecycleAssigned,
			OperationID: nil, Assignment: &state.AssignmentRecord{
				ID: "00000000-0000-4000-8000-000000000004", Owner: "owner", Hostname: "host",
				AssignedAt: now, RenewedAt: now, ExpiresAt: expires, LeaseDurationMinutes: 60,
				LastActivityAt: now, LeaseKeepers: []state.LeaseKeeperRecord{}, Ports: map[string]int64{},
			}, Processes: []state.TrackedProcessRecord{}, CreatedAt: now, UpdatedAt: now,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	adapters, err := NewMutationAdapters(MutationAdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapters.Sync(context.Background(), SyncCommandInput{Repository: repository, GuardSharedCheckout: true})
	var sharedErr *SharedCheckoutError
	if !errors.As(err, &sharedErr) || sharedErr.ActiveAssignments != 1 {
		t.Fatalf("Sync error = %v, want one active shared-checkout error", err)
	}
}

func seedAvailableWorkspace(t *testing.T, commonDir, path string) {
	t.Helper()
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(commonDir, lock.NewDirectoryLocker(lock.Config{}))
	now := "2026-08-16T12:00:00.000Z"
	available := now
	if err := store.Update(context.Background(), func(current *state.State) error {
		current.Workspaces[key] = state.WorkspaceRecord{
			Path: path, Managed: true, Branch: "agent/reusable", Lifecycle: state.LifecycleAvailable,
			OperationID: nil, Assignment: nil, Processes: []state.TrackedProcessRecord{},
			CreatedAt: now, UpdatedAt: now, AvailableAt: &available,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentProjectionsReturnsAnOwnedCopy(t *testing.T) {
	path := filepath.Join("pool", "workspace")
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &state.State{
		Trees: map[string]state.TreeRecord{key: {Projections: []string{"node_modules"}}},
		Workspaces: map[string]state.WorkspaceRecord{key: {
			Assignment: &state.AssignmentRecord{ID: "assignment-1"},
		}},
	}
	got := assignmentProjections(snapshot, "assignment-1")
	if len(got) != 1 || got[0] != "node_modules" {
		t.Fatalf("assignmentProjections = %v", got)
	}
	got[0] = "changed"
	if snapshot.Trees[key].Projections[0] != "node_modules" {
		t.Fatal("assignmentProjections returned state-owned storage")
	}
}

type createWorkspaceAdapterStub struct{}

func (createWorkspaceAdapterStub) Create(context.Context, string, string, string, bool) error {
	return nil
}
func (createWorkspaceAdapterStub) Remove(context.Context, string, bool) error { return nil }
