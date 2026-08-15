package dependencies

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

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
			wantErr:      "Dependency installation failed: pnpm install failed with exit code 7: lockfile mismatch", wantStderr: "lockfile mismatch\n", wantInvocation: true,
		},
		{
			name:      "runner failure is wrapped and retains cause",
			manager:   PackageManager{Name: "bun", Command: []string{"bun", "install"}, DependencyMode: "managed"},
			runnerErr: spawnFailure,
			wantErr:   "Dependency installation failed: spawn failed", wantCause: spawnFailure, wantInvocation: true,
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
