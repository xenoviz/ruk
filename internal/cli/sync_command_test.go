package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
