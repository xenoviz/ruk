// Package cli composes Ruk commands and their input and output contracts.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	updatepkg "github.com/xenoviz/ruk/internal/update"
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

// Options configures an Application.
type Options struct {
	Version      string
	Distribution updatepkg.Distribution
	Stdout       io.Writer
	Stderr       io.Writer
	Update       UpdateOperation
}

// UpdateOperation is injected so compatibility tests can exercise CLI output
// without network access or executable replacement.
type UpdateOperation func(context.Context, updatepkg.Options) (updatepkg.Result, error)

// Application executes Ruk commands.
type Application struct {
	version      string
	distribution updatepkg.Distribution
	stdout       io.Writer
	stderr       io.Writer
	update       UpdateOperation
}

// New creates a Ruk command application.
func New(options Options) *Application {
	stdout := options.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := options.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	distribution := options.Distribution
	if distribution == "" {
		distribution = updatepkg.DistributionPackage
	}
	updateOperation := options.Update
	if updateOperation == nil {
		updateOperation = func(ctx context.Context, options updatepkg.Options) (updatepkg.Result, error) {
			return updatepkg.Update(ctx, options, updatepkg.Hooks{})
		}
	}
	return &Application{
		version:      options.Version,
		distribution: distribution,
		stdout:       stdout,
		stderr:       stderr,
		update:       updateOperation,
	}
}

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
	if args[0] == "update" {
		invocation, err := Parse(args)
		if err != nil {
			return 1, err
		}
		result, err := application.update(ctx, updatepkg.Options{
			Distribution:   application.distribution,
			CurrentVersion: application.version,
			CheckOnly:      invocation.Check,
		})
		if err != nil {
			return 1, err
		}
		if invocation.JSON {
			if err := json.NewEncoder(application.stdout).Encode(result); err != nil {
				return 1, fmt.Errorf("write update result: %w", err)
			}
			return 0, nil
		}
		if _, err := io.WriteString(application.stdout, formatUpdate(result)); err != nil {
			return 1, fmt.Errorf("write update result: %w", err)
		}
		return 0, nil
	}
	return 1, errors.New("command is not implemented")
}

func formatUpdate(result updatepkg.Result) string {
	switch result.Status {
	case updatepkg.StatusUpToDate:
		return fmt.Sprintf("Ruk %s is up to date.\n", result.CurrentVersion)
	case updatepkg.StatusUpdateAvailable:
		return fmt.Sprintf("Ruk %s is available (current %s).\n", result.LatestVersion, result.CurrentVersion)
	case updatepkg.StatusScheduled:
		return fmt.Sprintf("Ruk %s is verified and will replace the current executable after this process exits.\n", result.LatestVersion)
	default:
		return fmt.Sprintf("Updated Ruk from %s to %s using %s.\n", result.CurrentVersion, result.LatestVersion, result.Method)
	}
}

func isHelpArgument(argument string) bool {
	return argument == "help" || argument == "--help" || argument == "-h"
}
