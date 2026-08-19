package dependencies

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

func TestInstallerHumanModeTeesStdioAndKeepsBoundedTails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	stdin := strings.NewReader("confirm\n")
	var request CommandRequest
	installer := Installer{
		Runner: func(_ context.Context, got CommandRequest) (CommandResult, error) {
			request = got
			return CommandResult{Stdout: strings.Repeat("o", 32), Stderr: strings.Repeat("e", 32)}, nil
		},
		DiagnosticLimit: 8, Stdin: stdin, Stdout: &stdout, Stderr: &stderr, InheritStdio: true,
	}
	result, err := installer.Prepare(context.Background(), t.TempDir(), PackageManager{Name: "npm", Command: []string{"npm", "install"}, DependencyMode: "managed"})
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	if request.Stdin != stdin || request.Stdout != &stdout || request.Stderr != &stderr || !request.InheritStdio {
		t.Fatalf("stdio request = %#v", request)
	}
	if len(result.Stdout) != 8 || len(result.Stderr) != 8 {
		t.Fatalf("bounded tails = (%q, %q)", result.Stdout, result.Stderr)
	}
}

func TestInstallerMachineReadableModeSuppressesStdio(t *testing.T) {
	var request CommandRequest
	installer := Installer{Runner: func(_ context.Context, got CommandRequest) (CommandResult, error) {
		request = got
		return CommandResult{}, nil
	}}
	if _, err := installer.Prepare(context.Background(), t.TempDir(), PackageManager{Name: "npm", Command: []string{"npm", "install"}, DependencyMode: "managed"}); err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	if request.InheritStdio || request.Stdout != nil || request.Stderr != nil || request.Stdin != nil {
		t.Fatalf("machine-readable request leaked stdio = %#v", request)
	}
}

type installerSupervisorStub struct {
	command []string
	options processpkg.RunOptions
}

func (stub *installerSupervisorStub) Run(_ context.Context, command []string, options processpkg.RunOptions) (processpkg.RunResult, error) {
	stub.command = append([]string(nil), command...)
	stub.options = options
	return processpkg.RunResult{Stdout: "installed\n"}, nil
}

func TestInstallerDefaultRunnerUsesNativeTreeSupervision(t *testing.T) {
	root := t.TempDir()
	supervisor := &installerSupervisorStub{}
	installer := Installer{Supervisor: supervisor}
	result, err := installer.Prepare(context.Background(), root, PackageManager{
		Name: "custom", Command: []string{"custom-installer", "install", "--frozen"}, DependencyMode: "managed",
	})
	if err != nil {
		t.Fatalf("Prepare returned an error: %v", err)
	}
	if result.Stdout != "installed\n" {
		t.Fatalf("installer output = %q, want supervisor output", result.Stdout)
	}
	if !reflect.DeepEqual(supervisor.command, []string{"custom-installer", "install", "--frozen"}) {
		t.Fatalf("supervisor command = %#v", supervisor.command)
	}
	if supervisor.options.Mode != processpkg.Detached || !supervisor.options.SuperviseCancellation {
		t.Fatalf("supervisor options = %#v, want detached cancellation supervision", supervisor.options)
	}
	if supervisor.options.Dir != root || supervisor.options.CaptureLimit != defaultDiagnosticLimit {
		t.Fatalf("supervisor directory/limit = %q/%d", supervisor.options.Dir, supervisor.options.CaptureLimit)
	}
}

func TestProcessRunNeedsRetentionForUncertainWait(t *testing.T) {
	err := &processpkg.ProcessWaitError{Cause: errors.New("wait status unavailable")}
	if !processRunNeedsRetention(err) {
		t.Fatal("uncertain wait failure must retain the installer process record")
	}
}

type installerRemovalSupervisor struct{}

func (installerRemovalSupervisor) Run(_ context.Context, _ []string, options processpkg.RunOptions) (processpkg.RunResult, error) {
	groupID := int64(42)
	record := state.TrackedProcessRecord{PID: 42, StartedAt: "native:42", GroupID: &groupID}
	if options.Register != nil {
		if err := options.Register(context.Background(), record); err != nil {
			return processpkg.RunResult{PID: 42, Started: true, Record: record}, err
		}
	}
	return processpkg.RunResult{PID: 42, Started: true, Record: record}, nil
}

type installerUnsafeRegistrationSupervisor struct{}

func (installerUnsafeRegistrationSupervisor) Run(ctx context.Context, _ []string, options processpkg.RunOptions) (processpkg.RunResult, error) {
	groupID := int64(43)
	record := state.TrackedProcessRecord{PID: 43, StartedAt: processpkg.UnverifiedIdentityMarker, GroupID: &groupID}
	if options.Register != nil {
		_ = options.Register(ctx, record)
	}
	return processpkg.RunResult{PID: 43, Started: true, Record: record}, &processpkg.ProcessCleanupUnsafeError{
		PID: 43, Mode: processpkg.Detached, Record: record, Cause: errors.New("installer descendants remain"),
	}
}

func TestInstallerRetriesRecoveryRecordAfterUnsafeRegistrationFailure(t *testing.T) {
	calls := 0
	var persisted state.TrackedProcessRecord
	_, err := runNativeInstallerCommand(context.Background(), CommandRequest{Command: "installer"}, installerUnsafeRegistrationSupervisor{}, func(_ context.Context, record state.TrackedProcessRecord) error {
		calls++
		if calls == 1 {
			return errors.New("transient state write failure")
		}
		persisted = record
		return nil
	}, nil)
	if err == nil {
		t.Fatal("unsafe supervised installer unexpectedly succeeded")
	}
	if calls != 2 || persisted.PID != 43 || persisted.StartedAt != processpkg.UnverifiedIdentityMarker {
		t.Fatalf("registration calls = %d, persisted = %#v, want exact bounded recovery retry", calls, persisted)
	}
}

type installerUnsafeSpawnSupervisor struct{}

func (installerUnsafeSpawnSupervisor) Run(context.Context, []string, processpkg.RunOptions) (processpkg.RunResult, error) {
	record := state.TrackedProcessRecord{PID: 44, StartedAt: processpkg.UnverifiedIdentityMarker, Command: []string{"installer"}}
	return processpkg.RunResult{}, &processpkg.ProcessCleanupUnsafeError{
		PID: 44, Mode: processpkg.Detached, Record: record, Cause: errors.New("Windows child wait remained unsafe"),
	}
}

func TestInstallerPersistsRecoveryRecordFromUnsafeSpawnError(t *testing.T) {
	var persisted state.TrackedProcessRecord
	_, err := runNativeInstallerCommand(context.Background(), CommandRequest{Command: "installer"}, installerUnsafeSpawnSupervisor{}, func(_ context.Context, record state.TrackedProcessRecord) error {
		persisted = record
		return nil
	}, nil)
	if err == nil {
		t.Fatal("unsafe spawn unexpectedly succeeded")
	}
	if persisted.PID != 44 || persisted.StartedAt != processpkg.UnverifiedIdentityMarker {
		t.Fatalf("persisted = %#v, want unsafe spawn recovery sentinel", persisted)
	}
}

func TestInstallerRemovalFailureIsBoundedAndRetained(t *testing.T) {
	removalErr := errors.New("state lock unavailable")
	var removalDeadline bool
	result, err := runNativeInstallerCommand(context.Background(), CommandRequest{Command: "installer"}, installerRemovalSupervisor{}, func(context.Context, state.TrackedProcessRecord) error {
		return nil
	}, func(ctx context.Context, _ state.TrackedProcessRecord) error {
		_, removalDeadline = ctx.Deadline()
		return removalErr
	})
	if result.ExitCode != 0 {
		t.Fatalf("result = %#v, want successful child status", result)
	}
	if err == nil || !errors.Is(err, removalErr) || !removalDeadline {
		t.Fatalf("error = %v, deadline = %v, want bounded retained removal failure", err, removalDeadline)
	}
	var unsafe *processpkg.ProcessCleanupUnsafeError
	if !errors.As(err, &unsafe) || unsafe.PID != 42 {
		t.Fatalf("error = %T %v, want retained process cleanup metadata", err, err)
	}
}

type installerCall struct {
	request CommandRequest
}

func TestInstallerPrepareTableDrivenContracts(t *testing.T) {
	t.Parallel()
	spawnFailure := errors.New("spawn failed")

	tests := []struct {
		name           string
		manager        PackageManager
		runnerResult   CommandResult
		runnerErr      error
		wantArgs       []string
		wantErr        string
		wantCause      error
		wantStdout     string
		wantStderr     string
		wantBunStore   string
		wantInvocation bool
	}{
		{
			name: "managed install succeeds",
			manager: PackageManager{
				Name: "bun", Version: "1.3.14", Command: []string{"bun", "install", "--frozen-lockfile"}, DependencyMode: "managed",
			},
			runnerResult: CommandResult{Stdout: "installed\n"},
			wantArgs:     []string{"install", "--frozen-lockfile"}, wantStdout: "installed\n", wantInvocation: true,
		},
		{
			name: "shared bun adds isolated linker and global store",
			manager: PackageManager{
				Name: "bun", Version: "bun 1.3.15", Command: []string{"bun", "install"}, DependencyMode: "shared",
			},
			runnerResult: CommandResult{},
			wantArgs:     []string{"install", "--linker", "isolated"}, wantBunStore: "1", wantInvocation: true,
		},
		{
			name: "shared pnpm accepts configured global store",
			manager: PackageManager{
				Name: "pnpm", Version: "10.12.1", Command: []string{"pnpm", "install", "--enable-global-virtual-store=true"}, DependencyMode: "shared",
			},
			runnerResult: CommandResult{},
			wantArgs:     []string{"install", "--enable-global-virtual-store=true"}, wantInvocation: true,
		},
		{
			name: "command failure is wrapped",
			manager: PackageManager{
				Name: "pnpm", Version: "10.12.1", Command: []string{"pnpm", "install"}, DependencyMode: "managed",
			},
			runnerResult: CommandResult{ExitCode: 7, Stderr: "lockfile mismatch\n"},
			wantArgs:     []string{"install"}, wantErr: "Dependency installation failed: pnpm install failed with exit code 7: lockfile mismatch", wantStderr: "lockfile mismatch\n", wantInvocation: true,
		},
		{
			name:      "runner failure is wrapped and retains cause",
			manager:   PackageManager{Name: "bun", Command: []string{"bun", "install"}, DependencyMode: "managed"},
			runnerErr: spawnFailure,
			wantArgs:  []string{"install"}, wantErr: "Dependency installation failed: spawn failed", wantCause: spawnFailure, wantInvocation: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := make([]installerCall, 0, 1)
			runner := func(_ context.Context, request CommandRequest) (CommandResult, error) {
				calls = append(calls, installerCall{request: request})
				return test.runnerResult, test.runnerErr
			}
			installer := Installer{Runner: runner, Environment: []string{"PATH=test", "BUN_INSTALL_GLOBAL_STORE=old"}}
			result, err := installer.Prepare(context.Background(), t.TempDir(), test.manager)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Prepare returned error: %v", err)
				}
			} else if err == nil || err.Error() != test.wantErr {
				t.Fatalf("Prepare error = %v, want %q", err, test.wantErr)
			}
			if len(calls) != boolInt(test.wantInvocation) {
				t.Fatalf("runner calls = %d, want %d", len(calls), boolInt(test.wantInvocation))
			}
			if len(calls) == 0 {
				return
			}
			if !reflect.DeepEqual(calls[0].request.Args, test.wantArgs) {
				t.Fatalf("args = %#v, want %#v", calls[0].request.Args, test.wantArgs)
			}
			if test.wantBunStore != "" && !containsEnvironment(calls[0].request.Env, "BUN_INSTALL_GLOBAL_STORE="+test.wantBunStore) {
				t.Fatalf("environment = %#v, want global store %q", calls[0].request.Env, test.wantBunStore)
			}
			if result.Stdout != test.wantStdout || result.Stderr != test.wantStderr {
				t.Fatalf("result diagnostics = (%q, %q), want (%q, %q)", result.Stdout, result.Stderr, test.wantStdout, test.wantStderr)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("error = %v, want cause %v", err, test.wantCause)
			}
		})
	}
}

func TestInstallerPrepareRejectsUnsupportedSharedModesAndVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		manager PackageManager
		wantErr string
	}{
		{name: "unsupported manager", manager: PackageManager{Name: "npm", Version: "11.0.0", Command: []string{"npm", "install"}, DependencyMode: "shared"}, wantErr: `Ruk's shared dependency backend does not support npm`},
		{name: "bun below minimum", manager: PackageManager{Name: "bun", Version: "1.3.13", Command: []string{"bun", "install"}, DependencyMode: "shared"}, wantErr: "bun 1.3.14 or newer is required"},
		{name: "pnpm unknown version", manager: PackageManager{Name: "pnpm", Version: "unknown", Command: []string{"pnpm", "install"}, DependencyMode: "shared"}, wantErr: "pnpm 10.12.1 or newer is required"},
		{name: "bun non-isolated linker", manager: PackageManager{Name: "bun", Version: "1.3.14", Command: []string{"bun", "install", "--linker=hoisted"}, DependencyMode: "shared"}, wantErr: "Bun's global virtual store requires the isolated linker"},
		{name: "pnpm disabled global store", manager: PackageManager{Name: "pnpm", Version: "10.12.1", Command: []string{"pnpm", "install", "--config.enable-global-virtual-store=false"}, DependencyMode: "shared"}, wantErr: "pnpm's shared dependency backend requires the global virtual store"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			_, err := Installer{Runner: func(context.Context, CommandRequest) (CommandResult, error) {
				called = true
				return CommandResult{}, nil
			}}.Prepare(context.Background(), t.TempDir(), test.manager)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
			if called {
				t.Fatal("runner called after preflight rejection")
			}
		})
	}
}

func TestInstallerPrepareCancellationAndBoundedDiagnostics(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelErr := errors.New("cancelled by runner")
	result, err := (Installer{
		Runner: func(context.Context, CommandRequest) (CommandResult, error) {
			return CommandResult{Stdout: strings.Repeat("o", 30), Stderr: strings.Repeat("e", 30)}, cancelErr
		}, DiagnosticLimit: 8,
	}).Prepare(ctx, t.TempDir(), PackageManager{Name: "bun", Command: []string{"bun", "install"}, DependencyMode: "managed"})
	if err == nil || !strings.Contains(err.Error(), "Dependency installation failed: cancelled by runner") {
		t.Fatalf("cancellation error = %v", err)
	}
	if len(result.Stdout) != 8 || result.Stdout != strings.Repeat("o", 8) || len(result.Stderr) != 8 || result.Stderr != strings.Repeat("e", 8) {
		t.Fatalf("bounded diagnostics = (%q, %q)", result.Stdout, result.Stderr)
	}
}

func TestAssertSharedBackendSupportedTableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		backend string
		version string
		wantErr bool
	}{
		{name: "bun minimum", backend: "bun", version: "1.3.14"},
		{name: "bun prefixed", backend: "bun", version: "bun v1.4.0"},
		{name: "pnpm minimum", backend: "pnpm", version: "10.12.1"},
		{name: "bun old", backend: "bun", version: "1.3.13", wantErr: true},
		{name: "pnpm missing", backend: "pnpm", version: "unknown", wantErr: true},
		{name: "unknown backend", backend: "yarn", version: "1.0.0", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := AssertSharedBackendSupported(test.backend, test.version)
			if (err != nil) != test.wantErr {
				t.Fatalf("AssertSharedBackendSupported() error = %v, want error %v", err, test.wantErr)
			}
		})
	}
}

func containsEnvironment(environment []string, wanted string) bool {
	for _, value := range environment {
		if value == wanted {
			return true
		}
	}
	return false
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
