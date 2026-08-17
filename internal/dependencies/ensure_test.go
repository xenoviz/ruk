package dependencies

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type ensureMemoryStore struct {
	state *state.State
}

func (store *ensureMemoryStore) Read(context.Context) (*state.State, error) { return store.state, nil }

func (store *ensureMemoryStore) Update(_ context.Context, mutate func(*state.State) error) error {
	return mutate(store.state)
}

type ensureLocker struct{}

func (ensureLocker) With(_ context.Context, _ string, callback func() error) error { return callback() }

type ensureInstaller func(context.Context, string, PackageManager) (InstallResult, error)

func (installer ensureInstaller) Prepare(ctx context.Context, root string, manager PackageManager) (InstallResult, error) {
	return installer(ctx, root, manager)
}

type ensureTrackingSupervisor struct {
	record state.TrackedProcessRecord
	err    error
}

func (supervisor ensureTrackingSupervisor) Run(ctx context.Context, _ []string, options processpkg.RunOptions) (processpkg.RunResult, error) {
	if options.Register != nil {
		if err := options.Register(ctx, supervisor.record); err != nil {
			return processpkg.RunResult{Record: supervisor.record}, err
		}
	}
	return processpkg.RunResult{Record: supervisor.record}, supervisor.err
}

func newEnsureFixture(t *testing.T) (EnsureInput, *ensureMemoryStore, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "package.json"), `{"name":"fixture"}`)
	store := &ensureMemoryStore{state: &state.State{
		Version:    state.CurrentVersion,
		Trees:      map[string]state.TreeRecord{},
		Workspaces: map[string]state.WorkspaceRecord{},
		Metrics:    state.EmptyMetrics(),
	}}
	input := EnsureInput{
		Repository: git.Repository{Root: root, CommonDir: root},
		Manager:    PackageManager{Name: "custom", Command: []string{"custom", "install"}, DependencyMode: "managed"},
		Files:      []string{"package.json"},
		Store:      store,
		Locker:     ensureLocker{},
		CurrentBranch: func(context.Context, string) (string, error) {
			return "main", nil
		},
		Now: func() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) },
	}
	return input, store, root
}

func TestEnsureDependenciesPreparesAndPublishesProjectionMetadata(t *testing.T) {
	input, store, root := newEnsureFixture(t)
	calls := 0
	input.Installer = ensureInstaller(func(_ context.Context, root string, manager PackageManager) (InstallResult, error) {
		calls++
		if manager.Name != "custom" || manager.DependencyMode != "managed" {
			t.Fatalf("installer manager = %#v", manager)
		}
		writeFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), "prepared")
		return InstallResult{Command: []string{"custom", "install"}}, nil
	})

	result, err := EnsureDependencies(context.Background(), input)
	if err != nil {
		t.Fatalf("EnsureDependencies returned an error: %v", err)
	}
	if calls != 1 || result.Reused || result.AlreadyAttached || result.Mode != "managed-install" || result.Fingerprint == "" {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
	key, err := state.TreeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	record := store.state.Trees[key]
	if record.Path != root || record.Mode != "managed-install" || record.Branch != "main" || record.ProjectionFingerprint == "" {
		t.Fatalf("tree record = %#v", record)
	}
	if store.state.Metrics.Preparations != 1 || store.state.Metrics.PreparationSkips != 0 {
		t.Fatalf("metrics = %#v", store.state.Metrics)
	}
}

func TestEnsureDependenciesRetainsUnsafePreparingInstallerProcess(t *testing.T) {
	input, store, root := newEnsureFixture(t)
	key, err := state.TreeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "preparation-operation"
	store.state.Workspaces[key] = state.WorkspaceRecord{
		Path: root, Managed: true, Branch: "agent/test", Lifecycle: state.LifecyclePreparing,
		OperationID: &operationID, Processes: []state.TrackedProcessRecord{},
		CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	record := state.TrackedProcessRecord{PID: 42, StartedAt: "native:42", Command: []string{"installer"}}
	input.Installer = Installer{
		Supervisor: ensureTrackingSupervisor{
			record: record,
			err:    &processpkg.ProcessCleanupUnsafeError{PID: 42, Mode: processpkg.Detached, Record: record, Cause: errors.New("descendants remain")},
		},
	}

	if _, err := EnsureDependencies(context.Background(), input); err == nil {
		t.Fatal("EnsureDependencies succeeded despite unsafe installer cleanup")
	}
	workspace := store.state.Workspaces[key]
	if len(workspace.Processes) != 1 || workspace.Processes[0].PID != record.PID || workspace.Processes[0].StartedAt != record.StartedAt {
		t.Fatalf("preparing workspace processes = %#v, want durable installer record", workspace.Processes)
	}
	if workspace.UpdatedAt != "2026-01-01T00:00:00.001Z" {
		t.Fatalf("workspace UpdatedAt = %q, want monotonic process-record fence", workspace.UpdatedAt)
	}
}

func TestEnsureDependenciesUsesSharedModeContract(t *testing.T) {
	input, _, _ := newEnsureFixture(t)
	input.Manager = PackageManager{
		Name: "bun", Version: "1.3.14", Command: []string{"bun", "install"}, DependencyMode: "shared",
	}
	input.Installer = ensureInstaller(func(_ context.Context, root string, manager PackageManager) (InstallResult, error) {
		if manager.DependencyMode != "shared" || manager.Name != "bun" {
			return InstallResult{}, errors.New("shared manager contract was not preserved")
		}
		writeFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), "shared")
		return InstallResult{}, nil
	})

	result, err := EnsureDependencies(context.Background(), input)
	if err != nil {
		t.Fatalf("EnsureDependencies returned an error: %v", err)
	}
	if result.Mode != "bun-global-store" {
		t.Fatalf("shared result = %#v", result)
	}
}

func TestEnsureDependenciesReusesIntactProjectionWithoutInstaller(t *testing.T) {
	input, store, root := newEnsureFixture(t)
	writeFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), "prepared")
	details, err := DependencyFingerprint(SourceFingerprintInput{Root: root, Files: input.Files, Manager: input.Manager})
	if err != nil {
		t.Fatal(err)
	}
	projectionFingerprint, err := ProjectionFingerprint(root, []string{"node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := state.TreeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	store.state.Trees[key] = state.TreeRecord{
		Path: root, Fingerprint: details.Fingerprint, ProjectionFingerprint: projectionFingerprint,
		Mode: "managed-install", Projections: []string{"node_modules"}, Branch: "main", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	input.Installer = ensureInstaller(func(context.Context, string, PackageManager) (InstallResult, error) {
		t.Fatal("installer ran for an intact projection")
		return InstallResult{}, nil
	})

	result, err := EnsureDependencies(context.Background(), input)
	if err != nil {
		t.Fatalf("EnsureDependencies returned an error: %v", err)
	}
	if !result.Reused || !result.AlreadyAttached || result.Mode != "managed-install" {
		t.Fatalf("result = %#v", result)
	}
	if store.state.Metrics.PreparationSkips != 1 || store.state.Metrics.Preparations != 0 {
		t.Fatalf("metrics = %#v", store.state.Metrics)
	}
}

func TestEnsureDependenciesInstallerFailurePreservesBoundedDiagnostics(t *testing.T) {
	input, _, _ := newEnsureFixture(t)
	input.Installer = Installer{
		DiagnosticLimit: 8,
		Environment:     []string{"PATH=test"},
		Runner: func(context.Context, CommandRequest) (CommandResult, error) {
			return CommandResult{Stdout: strings.Repeat("o", 32), Stderr: strings.Repeat("e", 32), ExitCode: 7}, nil
		},
	}
	_, err := EnsureDependencies(context.Background(), input)
	if err == nil {
		t.Fatal("EnsureDependencies succeeded after installer rejection")
	}
	var preparationErr *DependencyPreparationError
	if !errors.As(err, &preparationErr) {
		t.Fatalf("error = %T %v, want DependencyPreparationError", err, err)
	}
	if len(preparationErr.Stdout) != 8 || len(preparationErr.Stderr) != 8 || !strings.HasSuffix(preparationErr.Stderr, "eeeeeeee") {
		t.Fatalf("bounded diagnostics = %#v", preparationErr)
	}
}

func TestEnsureDependenciesRepairsCorruptProjection(t *testing.T) {
	input, store, root := newEnsureFixture(t)
	writeFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), "corrupt")
	details, err := DependencyFingerprint(SourceFingerprintInput{Root: root, Files: input.Files, Manager: input.Manager})
	if err != nil {
		t.Fatal(err)
	}
	key, err := state.TreeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	store.state.Trees[key] = state.TreeRecord{
		Path: root, Fingerprint: details.Fingerprint, ProjectionFingerprint: strings.Repeat("0", 64),
		Mode: "managed-install", Projections: []string{"node_modules"}, Branch: "main", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	called := false
	input.Installer = ensureInstaller(func(_ context.Context, root string, _ PackageManager) (InstallResult, error) {
		called = true
		writeFile(t, filepath.Join(root, "node_modules", "fixture", "index.js"), "repaired")
		return InstallResult{}, nil
	})

	result, err := EnsureDependencies(context.Background(), input)
	if err != nil {
		t.Fatalf("EnsureDependencies returned an error: %v", err)
	}
	if !called || result.Reused || result.Fingerprint == "" {
		t.Fatalf("result = %#v, called = %v", result, called)
	}
	record := store.state.Trees[key]
	if !ProjectionIntegrityValid(root, record.Projections, record.ProjectionFingerprint) {
		t.Fatalf("repaired record failed integrity validation: %#v", record)
	}
}
