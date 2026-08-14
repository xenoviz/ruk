package cli_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
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
