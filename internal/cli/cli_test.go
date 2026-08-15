package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
	updatepkg "github.com/xenoviz/ruk/internal/update"
)

const expectedHelp = `Ruk — dependency-aware Git workspaces for parallel coding agents

Usage:
  ruk init [--json]
  ruk create <branch> [--path <directory>] [--from <ref>] [--fetch] [--detach] [--json]
  ruk acquire <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] [--json]
  ruk renew <assignment-id> [--ttl <minutes>] [--json]
  ruk release <assignment-id> [--force] [--json]
  ruk sync [--allow-shared-checkout] [--json]
  ruk run [--allow-shared-checkout] -- <command> [args...]
  ruk exec <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] -- <command> [args...]
  ruk warm --count <n> [--from <ref>] [--fetch] [--json]
  ruk shell <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...]
  ruk list [--json]
  ruk remove <path> [--force]
  ruk status [--explain] [--json]
  ruk stats [--disk] [--json]
  ruk gc [--max-age <minutes>] [--apply] [--force-expired] [--json]
  ruk update [--check] [--json]

Ruk shares immutable package content by default when it automatically detects
supported Bun and pnpm versions. A custom installCommand defaults to managed
mode; set dependencyMode to "shared" explicitly only for a compatible custom
Bun or pnpm command.
`

func TestVersionReportsConfiguredVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	application := cli.New(cli.Options{
		Version: "0.3.0-test",
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	code, err := application.Run(context.Background(), []string{"--version"})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if code != 0 {
		t.Fatalf("Run returned exit code %d, want 0", code)
	}
	if got := stdout.String(); got != "0.3.0-test\n" {
		t.Fatalf("stdout = %q, want %q", got, "0.3.0-test\n")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty output", got)
	}
}

func TestUpdateCommandPreservesDistributionAndOutputContracts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		args         []string
		distribution updatepkg.Distribution
		result       updatepkg.Result
		wantOutput   string
	}{
		{
			name:         "human package update",
			args:         []string{"update"},
			distribution: updatepkg.DistributionPackage,
			result:       updatepkg.Result{Status: updatepkg.StatusUpdated, CurrentVersion: "0.2.0", LatestVersion: "0.3.0", Method: "npm"},
			wantOutput:   "Updated Ruk from 0.2.0 to 0.3.0 using npm.\n",
		},
		{
			name:         "human check",
			args:         []string{"update", "--check"},
			distribution: updatepkg.DistributionStandalone,
			result:       updatepkg.Result{Status: updatepkg.StatusUpdateAvailable, CurrentVersion: "0.2.0", LatestVersion: "0.3.0", Method: "standalone"},
			wantOutput:   "Ruk 0.3.0 is available (current 0.2.0).\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			called := false
			application := cli.New(cli.Options{
				Version:      "0.2.0",
				Distribution: test.distribution,
				Entrypoint:   "/package/bin/ruk",
				Stdout:       &stdout,
				Update: func(_ context.Context, options updatepkg.Options) (updatepkg.Result, error) {
					called = true
					if options.Distribution != test.distribution || options.CurrentVersion != "0.2.0" || options.CheckOnly != (len(test.args) == 2) || options.Entrypoint != "/package/bin/ruk" {
						t.Fatalf("update options = %#v", options)
					}
					return test.result, nil
				},
			})
			code, err := application.Run(context.Background(), test.args)
			if err != nil || code != 0 {
				t.Fatalf("Run = %d, %v", code, err)
			}
			if !called || stdout.String() != test.wantOutput {
				t.Fatalf("called=%v stdout=%q", called, stdout.String())
			}
		})
	}

	var stdout bytes.Buffer
	application := cli.New(cli.Options{
		Version:      "0.2.0",
		Distribution: updatepkg.DistributionStandalone,
		Stdout:       &stdout,
		Update: func(context.Context, updatepkg.Options) (updatepkg.Result, error) {
			asset := "ruk-linux-x64"
			return updatepkg.Result{Status: updatepkg.StatusUpdateAvailable, CurrentVersion: "0.2.0", LatestVersion: "0.3.0", Method: "standalone", Asset: &asset}, nil
		},
	})
	if code, err := application.Run(context.Background(), []string{"update", "--check", "--json"}); err != nil || code != 0 {
		t.Fatalf("JSON Run = %d, %v", code, err)
	}
	var record updatepkg.Result
	if err := json.Unmarshal(stdout.Bytes(), &record); err != nil {
		t.Fatalf("JSON output = %q: %v", stdout.String(), err)
	}
	if record.Status != updatepkg.StatusUpdateAvailable || record.Asset == nil || *record.Asset != "ruk-linux-x64" {
		t.Fatalf("JSON record = %#v", record)
	}
}

func TestHelpPreservesPublicContract(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"default": nil,
		"command": {"help"},
		"long":    {"--help"},
		"short":   {"-h"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			application := cli.New(cli.Options{
				Version: "0.3.0-test",
				Stdout:  &stdout,
				Stderr:  &stderr,
			})

			code, err := application.Run(context.Background(), args)
			if err != nil {
				t.Fatalf("Run returned an error: %v", err)
			}
			if code != 0 {
				t.Fatalf("Run returned exit code %d, want 0", code)
			}
			if got := stdout.String(); got != expectedHelp {
				t.Fatalf("stdout = %q, want %q", got, expectedHelp)
			}
			if got := stderr.String(); got != "" {
				t.Fatalf("stderr = %q, want empty output", got)
			}
		})
	}
}
