package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
)

func syncTestCommand(result dependencies.EnsureResult, ensure func(dependencies.EnsureInput), guard SharedCheckoutGuard) SyncCommand {
	return SyncCommand{
		ResolveManager: func(context.Context, string, config.Config) (dependencies.PackageManager, error) {
			return dependencies.PackageManager{Name: "custom", Command: []string{"custom", "install"}, DependencyMode: "managed"}, nil
		},
		ListFiles: func(context.Context, string) ([]string, error) {
			return []string{"package.json", "pnpm-lock.yaml"}, nil
		},
		Ensure: func(_ context.Context, input dependencies.EnsureInput) (dependencies.EnsureResult, error) {
			if ensure != nil {
				ensure(input)
			}
			return result, nil
		},
		Guard: guard,
	}
}

func syncTestInput(output *bytes.Buffer) SyncCommandInput {
	return SyncCommandInput{
		Repository: git.Repository{Root: "/repo", CommonDir: "/repo/.git", PrimaryRoot: "/repo", PrimaryCheckout: true},
		Emit:       true,
		Output:     output,
	}
}

func TestSyncCommandReadyNoOpPreservesResultAndHumanOutput(t *testing.T) {
	var output bytes.Buffer
	var gotInput dependencies.EnsureInput
	command := syncTestCommand(dependencies.EnsureResult{
		Fingerprint: "0123456789abcdef", Mode: "managed-install", Reused: true, AlreadyAttached: true,
	}, func(input dependencies.EnsureInput) { gotInput = input }, nil)

	result, err := command.Run(context.Background(), syncTestInput(&output))
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if result.Status != "ready" || result.Path != "/repo" || !result.Reused || !result.AlreadyAttached {
		t.Fatalf("result = %#v, want ready result with reuse metadata", result)
	}
	if output.String() != "Dependencies already ready for 0123456789ab (managed-install).\n" {
		t.Fatalf("human output = %q", output.String())
	}
	if gotInput.Repository.Root != "/repo" || gotInput.Manager.Name != "custom" || len(gotInput.Files) != 2 {
		t.Fatalf("ensure input = %#v", gotInput)
	}
}

func TestSyncCommandPreparationUsesInjectedContextAndHumanOutput(t *testing.T) {
	var output bytes.Buffer
	command := syncTestCommand(dependencies.EnsureResult{
		Fingerprint: "fedcba9876543210", Mode: "bun-global-store",
	}, nil, nil)
	input := syncTestInput(&output)
	input.Config = config.Config{SharedCheckoutPolicy: config.Allow}

	result, err := command.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if result.Status != "prepared" || result.Mode != "bun-global-store" {
		t.Fatalf("result = %#v", result)
	}
	if output.String() != "Dependencies prepared for fedcba987654 (bun-global-store).\n" {
		t.Fatalf("human output = %q", output.String())
	}
}

func TestSyncCommandInjectsRuntimeAndMachineReadableInstallerPolicy(t *testing.T) {
	var gotRuntime dependencies.RuntimeIdentity
	var gotMachineReadable bool
	command := syncTestCommand(dependencies.EnsureResult{Fingerprint: "fingerprint", Mode: "managed-install"}, func(input dependencies.EnsureInput) {
		gotRuntime = input.Runtime
		gotMachineReadable = input.MachineReadable
	}, nil)
	command.ResolveRuntime = func(context.Context, string, dependencies.PackageManager) (dependencies.RuntimeIdentity, error) {
		return dependencies.RuntimeIdentity{Runtime: "node", Version: "22.0.0", NativeABI: "127"}, nil
	}
	input := syncTestInput(&bytes.Buffer{})
	input.JSON = true
	if _, err := command.Run(context.Background(), input); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if gotRuntime.Version != "22.0.0" || gotRuntime.NativeABI != "127" || !gotMachineReadable {
		t.Fatalf("runtime/machine-readable input = %#v/%v", gotRuntime, gotMachineReadable)
	}
}

func TestSyncCommandRescansRepositoryInputsAfterInstall(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	command := syncTestCommand(dependencies.EnsureResult{}, nil, nil)
	command.ResolveRuntime = func(context.Context, string, dependencies.PackageManager) (dependencies.RuntimeIdentity, error) {
		return dependencies.RuntimeIdentity{Runtime: "node", Version: "22.0.0", NativeABI: "127"}, nil
	}
	command.ListFiles = func(_ context.Context, repositoryRoot string) ([]string, error) {
		files := []string{"package.json"}
		if _, err := os.Stat(filepath.Join(repositoryRoot, "pnpm-lock.yaml")); err == nil {
			files = append(files, "pnpm-lock.yaml")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return files, nil
	}
	command.Ensure = func(ctx context.Context, input dependencies.EnsureInput) (dependencies.EnsureResult, error) {
		before, err := dependencies.DependencyFingerprint(dependencies.SourceFingerprintInput{
			Root: input.Repository.Root, Files: input.Files, Manager: input.Manager, Runtime: input.Runtime,
		})
		if err != nil {
			return dependencies.EnsureResult{}, err
		}
		if input.ListFiles == nil {
			return dependencies.EnsureResult{}, errors.New("sync command did not provide post-install file lister")
		}
		// Model an installer creating a lockfile, then use the propagated lister
		// to make the same post-install inventory EnsureDependencies uses.
		if err := os.WriteFile(filepath.Join(input.Repository.Root, "pnpm-lock.yaml"), []byte("lockfile-v1\n"), 0o600); err != nil {
			return dependencies.EnsureResult{}, err
		}
		afterFiles, err := input.ListFiles(ctx, input.Repository.Root)
		if err != nil {
			return dependencies.EnsureResult{}, err
		}
		after, err := dependencies.DependencyFingerprint(dependencies.SourceFingerprintInput{
			Root: input.Repository.Root, Files: afterFiles, Manager: input.Manager, Runtime: input.Runtime,
		})
		if err != nil {
			return dependencies.EnsureResult{}, err
		}
		if before.Fingerprint == after.Fingerprint {
			return dependencies.EnsureResult{}, errors.New("post-install input rescan did not change dependency fingerprint")
		}
		return dependencies.EnsureResult{Fingerprint: after.Fingerprint, Mode: "managed-install"}, nil
	}

	input := syncTestInput(&bytes.Buffer{})
	input.Repository = git.Repository{Root: root, CommonDir: root, PrimaryRoot: root, PrimaryCheckout: true}
	if _, err := command.Run(context.Background(), input); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
}

func TestSyncCommandPreservesExplicitPostInstallLister(t *testing.T) {
	explicitCalls := 0
	explicit := dependencies.DependencyFileLister(func(context.Context, string) ([]string, error) {
		explicitCalls++
		return []string{"explicit-lock.yaml"}, nil
	})
	var got dependencies.DependencyFileLister
	command := syncTestCommand(dependencies.EnsureResult{Fingerprint: "fingerprint", Mode: "managed-install"}, func(input dependencies.EnsureInput) {
		got = input.ListFiles
	}, nil)
	input := syncTestInput(&bytes.Buffer{})
	input.Ensure.ListFiles = explicit
	if _, err := command.Run(context.Background(), input); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if got == nil {
		t.Fatal("explicit post-install lister was discarded")
	}
	if _, err := got(context.Background(), input.Repository.Root); err != nil {
		t.Fatalf("explicit lister returned an error: %v", err)
	}
	if explicitCalls != 1 {
		t.Fatalf("explicit lister calls = %d, want 1", explicitCalls)
	}
}

func TestSyncCommandGuardDenialSkipsDependencyPreparation(t *testing.T) {
	var output bytes.Buffer
	called := false
	command := syncTestCommand(dependencies.EnsureResult{}, func(dependencies.EnsureInput) {
		called = true
	}, func(context.Context, git.Repository, config.Config) error {
		return NewSharedCheckoutError(1)
	})
	input := syncTestInput(&output)
	input.GuardSharedCheckout = true

	_, err := command.Run(context.Background(), input)
	var shared *SharedCheckoutError
	if !errors.As(err, &shared) || shared.ActiveAssignments != 1 {
		t.Fatalf("error = %v, want shared-checkout denial", err)
	}
	if called {
		t.Fatal("dependency preparation ran after guard denial")
	}
	if output.Len() != 0 {
		t.Fatalf("output after denial = %q", output.String())
	}
}

func TestSyncCommandPrimaryFenceCoversGuardAndPreparation(t *testing.T) {
	fenced := false
	command := syncTestCommand(dependencies.EnsureResult{Fingerprint: "fingerprint", Mode: "managed-install"}, func(dependencies.EnsureInput) {
		if !fenced {
			t.Fatal("dependency preparation ran outside primary-checkout fence")
		}
	}, func(context.Context, git.Repository, config.Config) error {
		if !fenced {
			t.Fatal("shared-checkout guard ran outside primary-checkout fence")
		}
		return nil
	})
	command.PrimaryFence = func(_ context.Context, _ git.Repository, callback func() error) error {
		fenced = true
		defer func() { fenced = false }()
		return callback()
	}
	input := syncTestInput(&bytes.Buffer{})
	input.GuardSharedCheckout = true
	if _, err := command.Run(context.Background(), input); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if fenced {
		t.Fatal("primary-checkout fence remained held after sync")
	}
}

func TestSyncCommandWarnPolicyWritesOnlyStderrDiagnostic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	command := syncTestCommand(dependencies.EnsureResult{Fingerprint: "fingerprint", Mode: "managed-install"}, nil, func(context.Context, git.Repository, config.Config) error {
		return &SharedCheckoutWarning{ActiveAssignments: 2}
	})
	command.PrimaryFence = func(context.Context, git.Repository, func() error) error {
		t.Fatal("warn policy acquired the long-running primary fence")
		return nil
	}
	input := syncTestInput(&stdout)
	input.Config = config.Config{SharedCheckoutPolicy: config.Warn}
	input.GuardSharedCheckout = true
	input.JSON = true
	input.Stderr = &stderr
	if _, err := command.Run(context.Background(), input); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if !strings.Contains(stderr.String(), "continuing because sharedCheckoutPolicy is warn") {
		t.Fatalf("stderr warning = %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "sharedCheckoutPolicy") {
		t.Fatalf("stdout contains warning: %q", stdout.String())
	}
}

func TestSyncCommandAllowSharedCheckoutBypassesGuardAndJSONSuppressesHumanProgress(t *testing.T) {
	var output bytes.Buffer
	guardCalled := false
	command := syncTestCommand(dependencies.EnsureResult{
		Fingerprint: "fingerprint", Mode: "managed-install", Reused: false,
	}, nil, func(context.Context, git.Repository, config.Config) error {
		guardCalled = true
		return NewSharedCheckoutError(1)
	})
	input := syncTestInput(&output)
	input.GuardSharedCheckout = true
	input.AllowSharedCheckout = true
	input.JSON = true

	result, err := command.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if guardCalled {
		t.Fatal("guard ran despite --allow-shared-checkout")
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output = %q: %v", output.String(), err)
	}
	if output.String() != "{\"status\":\"prepared\",\"path\":\"/repo\",\"fingerprint\":\"fingerprint\",\"mode\":\"managed-install\"}\n" {
		t.Fatalf("JSON output = %q, want stable public sync fields", output.String())
	}
	if result.Reused || result.AlreadyAttached {
		t.Fatalf("returned result = %#v, want internal reuse metadata preserved", result)
	}
	if strings.Contains(output.String(), "Dependencies prepared") {
		t.Fatalf("JSON output contains human progress: %q", output.String())
	}
}

func TestSyncCommandDependencyFailureIsReturnedWithoutSuccessOutput(t *testing.T) {
	var output bytes.Buffer
	want := errors.New("installer failed")
	command := syncTestCommand(dependencies.EnsureResult{}, nil, nil)
	command.Ensure = func(context.Context, dependencies.EnsureInput) (dependencies.EnsureResult, error) {
		return dependencies.EnsureResult{}, want
	}

	_, err := command.Run(context.Background(), syncTestInput(&output))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want dependency failure", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output after dependency failure = %q", output.String())
	}
}
