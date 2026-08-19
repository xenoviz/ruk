package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/dependencies"
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

func (stub *runtimeWorkspaceStub) Lock(context.Context, string) error { return nil }

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
	syncJSON := false
	clock := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		Now:   func() time.Time { return clock },
		NewID: func() string { return "00000000-0000-4000-8000-000000000001" },
		Sync: func(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
			syncJSON = input.JSON
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
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) { return "HEAD", nil },
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
	if !syncJSON {
		t.Fatal("JSON acquire did not propagate machine-readable sync policy")
	}
}

func TestAssignedSyncSnapshotsAssignmentAndRevalidatesBeforePreparation(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "workspace"), CommonDir: filepath.Join(root, "common")}
	if err := os.MkdirAll(repository.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(repository.CommonDir, lock.NewDirectoryLocker(lock.Config{}))
	key, err := state.TreeKey(repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(current *state.State) error {
		now := "2026-08-16T12:00:00.000Z"
		current.Workspaces[key] = state.WorkspaceRecord{
			Path: repository.Root, Managed: true, Branch: "agent/assigned", Lifecycle: state.LifecycleAssigned,
			Assignment: &state.AssignmentRecord{
				ID: "00000000-0000-4000-8000-000000000101", Owner: "owner", Hostname: "host",
				AssignedAt: now, RenewedAt: now, ExpiresAt: "2026-08-16T13:00:00.000Z", LeaseDurationMinutes: 60,
				LastActivityAt: now, LeaseKeepers: []state.LeaseKeeperRecord{}, Ports: map[string]int64{},
			},
			Processes: []state.TrackedProcessRecord{}, CreatedAt: now, UpdatedAt: now,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	beforeCalled := false
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		Sync: func(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
			if input.Ensure.BeforePrepare == nil || input.Ensure.Store == nil || input.Ensure.Locker == nil {
				return SyncCommandResult{}, errors.New("assigned sync did not receive fenced ensure seams")
			}
			if err := input.Ensure.Store.Update(ctx, func(current *state.State) error {
				workspace := current.Workspaces[key]
				workspace.Assignment.ID = "00000000-0000-4000-8000-000000000102"
				current.Workspaces[key] = workspace
				return nil
			}); err != nil {
				return SyncCommandResult{}, err
			}
			if err := input.Ensure.BeforePrepare(); err == nil || !strings.Contains(err.Error(), "00000000-0000-4000-8000-000000000101") {
				return SyncCommandResult{}, fmt.Errorf("before-prepare accepted replacement: %v", err)
			}
			beforeCalled = true
			return SyncCommandResult{}, errors.New("expected fenced assignment rejection")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapters.Sync(context.Background(), SyncCommandInput{Repository: repository})
	if err == nil || !beforeCalled || !strings.Contains(err.Error(), "expected fenced assignment rejection") {
		t.Fatalf("sync error=%v beforeCalled=%v", err, beforeCalled)
	}
}

func TestNewMutationAdaptersCreateUsesWorkspaceFence(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	workspace := &createWorkspaceStub{}
	fenceCalled := false
	preparationLockerCalled := false
	var fencedPath string
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) { return "resolved-start", nil },
		CreateWorkspace:    func(git.Repository) (CreateWorkspace, error) { return workspace, nil },
		CreateFence: func(_ context.Context, path string, operation func() error) error {
			fenceCalled = true
			fencedPath = path
			return operation()
		},
		Sync: func(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
			if input.Repository.Root != fencedPath {
				return SyncCommandResult{}, fmt.Errorf("sync root=%q, want fenced path %q", input.Repository.Root, fencedPath)
			}
			if input.Ensure.Store == nil || input.Ensure.Locker == nil {
				return SyncCommandResult{}, errors.New("create preparation did not receive held lock seams")
			}
			lockPath, err := MutationWorkspaceLockPath(input.Repository.CommonDir, input.Repository.Root)
			if err != nil {
				return SyncCommandResult{}, err
			}
			if err := input.Ensure.Locker.With(ctx, lockPath, func() error {
				preparationLockerCalled = true
				return nil
			}); err != nil {
				return SyncCommandResult{}, err
			}
			return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "slot")
	result, err := adapters.Create(context.Background(), CreateCommandInput{
		Repository: repository, CWD: root, Branch: "agent/fenced", Path: path,
	})
	if err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if !fenceCalled || !preparationLockerCalled || fencedPath != path || result.Path != path || !workspace.created {
		t.Fatalf("fenceCalled=%v preparationLockerCalled=%v fencedPath=%q result=%#v workspace=%#v", fenceCalled, preparationLockerCalled, fencedPath, result, workspace)
	}
}

func TestAcquirePreparationDoesNotReenterWorkspaceHandoffLock(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	workspace := &runtimeWorkspaceStub{}
	preparationCalled := false
	syncJSON := true
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		Now:   func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
		NewID: func() string { return "00000000-0000-4000-8000-000000000020" },
		Sync: func(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
			syncJSON = input.JSON
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
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) { return "HEAD", nil },
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
	if syncJSON {
		t.Fatal("human acquire marked sync preparation machine-readable")
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
				Now:   func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
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
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) { return "HEAD", nil },
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
	path := t.TempDir()
	if err := os.MkdirAll(filepath.Join(path, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectionFingerprint, err := dependencies.ProjectionFingerprint(path, []string{"node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &state.State{
		Trees: map[string]state.TreeRecord{key: {Path: path, ProjectionFingerprint: projectionFingerprint, Projections: []string{"node_modules"}}},
		Workspaces: map[string]state.WorkspaceRecord{key: {
			Path:       path,
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

func TestAssignmentProjectionsDropsCorruptProjection(t *testing.T) {
	path := t.TempDir()
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &state.State{
		Trees:      map[string]state.TreeRecord{key: {Path: path, ProjectionFingerprint: "stale", Projections: []string{"node_modules"}}},
		Workspaces: map[string]state.WorkspaceRecord{key: {Path: path, Assignment: &state.AssignmentRecord{ID: "assignment-1"}}},
	}
	if got := assignmentProjections(snapshot, "assignment-1"); got != nil {
		t.Fatalf("assignmentProjections = %v, want nil for corrupt projection", got)
	}
}

type createWorkspaceAdapterStub struct{}

func (createWorkspaceAdapterStub) Create(context.Context, string, string, string, bool) error {
	return nil
}
func (createWorkspaceAdapterStub) Remove(context.Context, string, bool) error { return nil }

func TestMutationRoutesRecordCreatedWorktrees(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	recorder := &capturingWorktreeRecorder{}
	factory := func(context.Context, git.Repository) (WorktreeRecorder, error) { return recorder, nil }
	clock := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	syncOK := func(context.Context, SyncCommandInput) (SyncCommandResult, error) {
		return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
	}

	createWorkspace := &createWorkspaceStub{}
	createAdapters, err := NewMutationAdapters(MutationAdapterOptions{
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) { return "HEAD", nil },
		CreateWorkspace:    func(git.Repository) (CreateWorkspace, error) { return createWorkspace, nil },
		WorktreeRecorder:   factory,
		Sync:               syncOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdPath := filepath.Join(root, "created")
	if _, err := createAdapters.Create(context.Background(), CreateCommandInput{
		Repository: repository, CWD: root, Branch: "agent/create", Path: createdPath,
	}); err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	if len(recorder.records) != 1 || recorder.records[0].source != state.WorktreeSourceCreate || recorder.records[0].path != createdPath {
		t.Fatalf("create records = %#v", recorder.records)
	}

	acquireWorkspace := &runtimeWorkspaceStub{}
	acquireAdapters, err := NewMutationAdapters(MutationAdapterOptions{
		Now:   func() time.Time { return clock },
		NewID: func() string { return "00000000-0000-4000-8000-000000000031" },
		Sync:  syncOK,
		AcquireWorktree: func(git.Repository) (lifecycle.AcquisitionWorktree, error) {
			return acquireWorkspace, nil
		},
		PortRegistry: func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
			return nil, nil, nil
		},
		WorktreeRecorder:   factory,
		StartPointResolver: func(context.Context, git.Repository, string, bool) (string, error) { return "HEAD", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireAdapters.Acquire(context.Background(), repository, AcquireInput{
		Branch: "agent/acquire", Owner: "owner", Hostname: "host", Now: clock,
	}); err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	if len(recorder.records) < 2 {
		t.Fatalf("acquire did not record a worktree: %#v", recorder.records)
	}
	foundAcquire := false
	for _, record := range recorder.records {
		if record.source == state.WorktreeSourceAcquire {
			foundAcquire = true
			break
		}
	}
	if !foundAcquire {
		t.Fatalf("acquire records missing source acquire: %#v", recorder.records)
	}
}

func TestRemoveRepositoryFailsWhenWorktreeRecorderIsUnavailable(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: filepath.Join(root, "repo"), CommonDir: filepath.Join(root, "common")}
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		WorktreeRecorder: func(context.Context, git.Repository) (WorktreeRecorder, error) {
			return nil, errors.New("recorder unavailable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = adapters.Remove(context.Background(), RemoveInput{
		Repository: repository, CWD: root, Path: filepath.Join(root, "slot"),
	})
	if err == nil || !strings.Contains(err.Error(), "recorder unavailable") {
		t.Fatalf("Remove error = %v, want recorder failure", err)
	}
}

type idleReleaseProcesses struct{}

func (idleReleaseProcesses) Exists(context.Context, state.TrackedProcessRecord) (bool, error) {
	return false, nil
}

func (idleReleaseProcesses) Terminate(context.Context, state.TrackedProcessRecord, bool) (bool, error) {
	return true, nil
}

func runRepoGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Ruk Test",
		"GIT_AUTHOR_EMAIL=ruk@example.test",
		"GIT_COMMITTER_NAME=Ruk Test",
		"GIT_COMMITTER_EMAIL=ruk@example.test",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func initPinnedStartPointRepo(t *testing.T) (git.Repository, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root := t.TempDir()
	runRepoGit(t, root, "init", "-q")
	runRepoGit(t, root, "config", "user.name", "Ruk Test")
	runRepoGit(t, root, "config", "user.email", "ruk@example.test")
	runRepoGit(t, root, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("commit A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRepoGit(t, root, "add", "README")
	runRepoGit(t, root, "commit", "-q", "-m", "A")
	sha := runRepoGit(t, root, "rev-parse", "HEAD")
	return git.Repository{
		Root: root, CommonDir: filepath.Join(root, ".git"),
		PrimaryRoot: root, PrimaryCheckout: true,
	}, sha
}

func TestDefaultAcquireStartPointPinsInvokingCheckoutHEAD(t *testing.T) {
	repository, sha := initPinnedStartPointRepo(t)
	got, err := defaultAcquireStartPoint(context.Background(), repository, "", false)
	if err != nil {
		t.Fatalf("defaultAcquireStartPoint returned an error: %v", err)
	}
	if got != sha {
		t.Fatalf("pinned HEAD = %q, want invoking checkout %q", got, sha)
	}
}

func TestDefaultAcquireStartPointResolvesRequestedSHAAndRejectsUnknownRef(t *testing.T) {
	repository, sha := initPinnedStartPointRepo(t)
	got, err := defaultAcquireStartPoint(context.Background(), repository, sha, false)
	if err != nil {
		t.Fatalf("defaultAcquireStartPoint(existing SHA) returned an error: %v", err)
	}
	if got != sha {
		t.Fatalf("pinned SHA = %q, want full commit %q", got, sha)
	}
	_, err = defaultAcquireStartPoint(context.Background(), repository, "refs/heads/does-not-exist", false)
	if err == nil {
		t.Fatal("unknown ref was accepted")
	}
}

type idleReleasePorter struct{}

func (idleReleasePorter) Release(context.Context, string) error { return nil }

func TestAcquireReusePinsStartPointFromInvokingCheckoutNotStaleSlotHEAD(t *testing.T) {
	repository, commitA := initPinnedStartPointRepo(t)
	clock := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	var id int
	adapters, err := NewMutationAdapters(MutationAdapterOptions{
		Now:   func() time.Time { return clock },
		NewID: func() string { id++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", id) },
		Sync: func(context.Context, SyncCommandInput) (SyncCommandResult, error) {
			return SyncCommandResult{Status: "prepared", Fingerprint: "fingerprint", Mode: "managed-install"}, nil
		},
		PortRegistry: func(*state.Store, string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
			return nil, idleReleasePorter{}, nil
		},
		ReleaseProcesses: func() lifecycle.ReleaseProcesser { return idleReleaseProcesses{} },
		WorktreeRecorder: func(context.Context, git.Repository) (WorktreeRecorder, error) {
			return &capturingWorktreeRecorder{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := adapters.Acquire(context.Background(), repository, AcquireInput{
		Branch: "agent/task-1", Owner: "owner", Hostname: "host", Now: clock,
	})
	if err != nil {
		t.Fatalf("first Acquire returned an error: %v", err)
	}
	if first.Reused || first.Path == "" {
		t.Fatalf("first acquire = %#v, want a fresh workspace", first.AcquireRecord)
	}
	if _, err := adapters.Release(context.Background(), ReleaseInput{
		Repository: repository, AssignmentID: first.AssignmentID, Force: true,
	}); err != nil {
		t.Fatalf("Release returned an error: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repository.Root, "README"), []byte("commit B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runRepoGit(t, repository.Root, "add", "README")
	runRepoGit(t, repository.Root, "commit", "-q", "-m", "B")
	commitB := runRepoGit(t, repository.Root, "rev-parse", "HEAD")
	if commitB == commitA {
		t.Fatal("primary checkout did not advance")
	}

	second, err := adapters.Acquire(context.Background(), repository, AcquireInput{
		Branch: "agent/task-5", Owner: "owner", Hostname: "host", Now: clock,
	})
	if err != nil {
		t.Fatalf("second Acquire returned an error: %v", err)
	}
	if !second.Reused || second.Path != first.Path {
		t.Fatalf("second acquire = %#v, want reuse of %s", second.AcquireRecord, first.Path)
	}
	got := runRepoGit(t, second.Path, "rev-parse", "HEAD")
	if got != commitB {
		t.Fatalf("reused worktree HEAD = %q, want invoking checkout %q (not stale %q)", got, commitB, commitA)
	}
}
