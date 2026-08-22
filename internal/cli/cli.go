// Package cli composes Ruk commands and their input and output contracts.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const helpText = `Ruk — dependency-aware Git workspaces for parallel coding agents

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
  ruk worktrees [--all] [--json]
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

// Run executes one Ruk command.
func (application *Application) Run(ctx context.Context, args []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 1, fmt.Errorf("run command: %w", err)
	}
	if len(args) == 0 || (len(args) == 1 && isHelpArgument(args[0])) {
		if _, err := io.WriteString(application.stdout, helpText); err != nil {
			return 1, fmt.Errorf("write help: %w", err)
		}
		return 0, nil
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		if _, err := fmt.Fprintln(application.stdout, application.version); err != nil {
			return 1, fmt.Errorf("write version: %w", err)
		}
		return 0, nil
	}
	invocation, err := Parse(args)
	if err != nil {
		return 1, err
	}
	switch invocation.Name {
	case "worktrees":
		if invocation.All {
			return application.runWorktreesAll(ctx, invocation)
		}
		return application.runQueries(ctx, invocation)
	case "update":
		return application.runUpdate(ctx, invocation)
	case "init", "sync":
		return application.runInitSync(ctx, invocation)
	case "create":
		return application.runCreate(ctx, invocation)
	case "acquire":
		return application.withRepository(ctx, invocation, application.runAcquire)
	case "release":
		return application.withRepository(ctx, invocation, application.runRelease)
	case "remove":
		return application.withRepository(ctx, invocation, application.runRemove)
	case "warm":
		return application.withRepository(ctx, invocation, application.runWarm)
	case "gc":
		return application.withRepository(ctx, invocation, application.runGC)
	case "run":
		return application.withRepository(ctx, invocation, application.runRun)
	case "exec":
		return application.withRepository(ctx, invocation, application.runExec)
	case "shell":
		return application.withRepository(ctx, invocation, application.runShell)
	case "renew":
		return application.withRepository(ctx, invocation, application.runRenew)
	case "list", "status", "stats":
		return application.runQueries(ctx, invocation)
	default:
		return 1, errors.New("command is not implemented")
	}
}
