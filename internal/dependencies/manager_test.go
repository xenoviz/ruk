package dependencies

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/config"
)

func TestResolvePackageManagerProbesSharedBunAndPnpmVersions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command []string
		output  string
		want    string
	}{
		{name: "bun", command: []string{"bun", "install"}, output: "bun 1.3.14\n", want: "1.3.14"},
		{name: "pnpm", command: []string{"pnpm", "install"}, output: "10.12.2\n", want: "10.12.2"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var requests []CommandRequest
			mode := config.Shared
			manager, err := ResolvePackageManager(context.Background(), t.TempDir(), config.Config{
				InstallCommand: testCase.command,
				DependencyMode: &mode,
			}, func(_ context.Context, request CommandRequest) (CommandResult, error) {
				requests = append(requests, request)
				return CommandResult{Stdout: testCase.output}, nil
			})
			if err != nil {
				t.Fatalf("ResolvePackageManager returned an error: %v", err)
			}
			if manager.Name != testCase.name || manager.Version != testCase.want || manager.DependencyMode != string(config.Shared) {
				t.Fatalf("manager = %#v, want %s shared %s", manager, testCase.name, testCase.want)
			}
			if len(requests) != 1 || requests[0].Command != testCase.name || strings.Join(requests[0].Args, " ") != "--version" {
				t.Fatalf("version probe requests = %#v", requests)
			}
		})
	}
}

func TestResolvePackageManagerVersionProbeFailureIsBoundedAndInjected(t *testing.T) {
	t.Parallel()
	probeFailure := errors.New("probe unavailable")
	var request CommandRequest
	mode := config.Shared
	_, err := (ManagerResolver{
		Runner: func(_ context.Context, got CommandRequest) (CommandResult, error) {
			request = got
			return CommandResult{Stdout: strings.Repeat("o", 100), Stderr: strings.Repeat("e", 100)}, probeFailure
		},
		DiagnosticLimit: 8,
	}).Resolve(context.Background(), t.TempDir(), config.Config{
		InstallCommand: []string{"bun", "install"},
		DependencyMode: &mode,
	})
	if err == nil || !strings.Contains(err.Error(), "Could not inspect bun") || !errors.Is(err, probeFailure) {
		t.Fatalf("error = %v, want wrapped probe failure", err)
	}
	if request.Command != "bun" || len(request.Args) != 1 || request.Args[0] != "--version" {
		t.Fatalf("request = %#v, want injected bun --version", request)
	}
}

func TestResolvePackageManagerTreatsNonZeroVersionProbeAsUnknown(t *testing.T) {
	t.Parallel()
	mode := config.Shared
	_, err := ResolvePackageManager(context.Background(), t.TempDir(), config.Config{
		InstallCommand: []string{"pnpm", "install"},
		DependencyMode: &mode,
	}, func(context.Context, CommandRequest) (CommandResult, error) {
		return CommandResult{ExitCode: 17, Stderr: "version failed"}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "pnpm 10.12.1 or newer is required") || !strings.Contains(err.Error(), "found unknown") {
		t.Fatalf("non-zero version probe error = %v, want unknown-version validation", err)
	}
}

func TestResolvePackageManagerParsesNoisyBoundedVersionOutput(t *testing.T) {
	t.Parallel()
	mode := config.Shared
	noisy := strings.Repeat("noise ", 40) + "pnpm v10.12.1"
	manager, err := (ManagerResolver{
		DiagnosticLimit: 32,
		Runner: func(context.Context, CommandRequest) (CommandResult, error) {
			return CommandResult{Stdout: noisy}, nil
		},
	}).Resolve(context.Background(), t.TempDir(), config.Config{
		InstallCommand: []string{"pnpm", "install"},
		DependencyMode: &mode,
	})
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	if manager.Version != "10.12.1" {
		t.Fatalf("version = %q, want canonical noisy-output version", manager.Version)
	}
}

func TestResolvePackageManagerDoesNotProbeCustomOrManagedInstallers(t *testing.T) {
	t.Parallel()
	called := false
	runner := func(context.Context, CommandRequest) (CommandResult, error) {
		called = true
		return CommandResult{}, errors.New("must not probe")
	}
	cases := []config.Config{
		{InstallCommand: []string{"custom-wrapper", "bootstrap"}},
		{InstallCommand: []string{"bun", "install"}},
	}
	for _, cfg := range cases {
		manager, err := ResolvePackageManager(context.Background(), t.TempDir(), cfg, runner)
		if err != nil {
			t.Fatalf("ResolvePackageManager returned an error: %v", err)
		}
		if manager.Version != UnknownManagerVersion || manager.DependencyMode != string(config.Managed) {
			t.Fatalf("manager = %#v, want deterministic managed manager", manager)
		}
	}
	if called {
		t.Fatal("resolver probed a custom or managed installer")
	}
}

func TestResolvePackageManagerValidatesSharedBackendAndMinimumVersion(t *testing.T) {
	t.Parallel()
	tooOld := "1.3.13"
	mode := config.Shared
	_, err := ResolvePackageManager(context.Background(), t.TempDir(), config.Config{
		InstallCommand: []string{"bun", "install"},
		DependencyMode: &mode,
	}, func(context.Context, CommandRequest) (CommandResult, error) {
		return CommandResult{Stdout: tooOld}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "bun 1.3.14 or newer is required") {
		t.Fatalf("old Bun error = %v", err)
	}

	_, err = ResolvePackageManager(context.Background(), t.TempDir(), config.Config{
		InstallCommand: []string{"custom-wrapper", "bootstrap"},
		DependencyMode: &mode,
	})
	if err == nil || !strings.Contains(err.Error(), "does not support custom-wrapper") {
		t.Fatalf("unsupported shared manager error = %v", err)
	}
}
