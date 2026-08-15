// Package cli composes Ruk commands and their input and output contracts.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
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
	Version            string
	Distribution       updatepkg.Distribution
	Stdout             io.Writer
	Stderr             io.Writer
	Update             UpdateOperation
	Renew              RepositoryRenewOperation
	Sync               SyncRouteOperation
	Create             CreateRouteOperation
	Acquire            AcquireRouteOperation
	Release            ReleaseRouteOperation
	Remove             RemoveRouteOperation
	CWD                string
	DiscoverRepository RepositoryDiscovery
	Queries            QueryDependencies
	Now                func() time.Time
}

// UpdateOperation is injected so compatibility tests can exercise CLI output
// without network access or executable replacement.
type UpdateOperation func(context.Context, updatepkg.Options) (updatepkg.Result, error)

// RepositoryRenewOperation performs one lifecycle renewal in the discovered
// repository while keeping state composition out of compatibility tests.
type RepositoryRenewOperation func(context.Context, git.Repository, string, time.Time) (state.WorkspaceRecord, error)

// SyncRouteOperation executes init/sync after repository discovery. The
// operation owns rendering when SyncCommandInput.Emit is true.
type SyncRouteOperation func(context.Context, SyncCommandInput) (SyncCommandResult, error)

// CreateRouteOperation executes create with the discovered repository. The
// input carries stdout so the service emits its result exactly once.
type CreateRouteOperation func(context.Context, CreateCommandInput) (CreateCommandResult, error)

// AcquireRouteOperation executes acquire against one discovered repository.
type AcquireRouteOperation func(context.Context, git.Repository, AcquireInput) (AcquireResult, error)

// ReleaseRouteOperation executes release and returns its rendered result.
type ReleaseRouteOperation func(context.Context, ReleaseInput) (ReleaseResult, error)

// RemoveRouteOperation performs remove, which has no success output.
type RemoveRouteOperation func(context.Context, RemoveInput) error

// RepositoryDiscovery resolves the current checkout without coupling the
// command router to Git subprocesses in compatibility tests.
type RepositoryDiscovery func(context.Context, string) (git.Repository, error)

// Application executes Ruk commands.
type Application struct {
	version      string
	distribution updatepkg.Distribution
	stdout       io.Writer
	stderr       io.Writer
	update       UpdateOperation
	renew        RepositoryRenewOperation
	sync         SyncRouteOperation
	create       CreateRouteOperation
	acquire      AcquireRouteOperation
	release      ReleaseRouteOperation
	remove       RemoveRouteOperation
	cwd          string
	discover     RepositoryDiscovery
	queries      QueryDependencies
	now          func() time.Time
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
	renewOperation := options.Renew
	if renewOperation == nil {
		renewOperation = defaultRenewOperation
	}
	cwd := options.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
		if cwd == "" {
			cwd = "."
		}
	}
	discover := options.DiscoverRepository
	if discover == nil {
		discover = func(ctx context.Context, cwd string) (git.Repository, error) {
			return git.Discover(ctx, cwd, nil)
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Application{
		version:      options.Version,
		distribution: distribution,
		stdout:       stdout,
		stderr:       stderr,
		update:       updateOperation,
		renew:        renewOperation,
		sync:         options.Sync,
		create:       options.Create,
		acquire:      options.Acquire,
		release:      options.Release,
		remove:       options.Remove,
		cwd:          cwd,
		discover:     discover,
		queries:      mergeQueryDependencies(options.Queries, defaultQueryDependencies()),
		now:          now,
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
	invocation, err := Parse(args)
	if err != nil {
		return 1, err
	}
	if invocation.Name == "update" {
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
	if invocation.Name == "init" || invocation.Name == "sync" {
		repository, err := application.discover(ctx, application.cwd)
		if err != nil {
			return 1, err
		}
		if application.sync == nil {
			return 1, errors.New("sync command is not configured")
		}
		_, err = application.sync(ctx, SyncCommandInput{
			Repository:          repository,
			JSON:                invocation.JSON,
			Emit:                true,
			GuardSharedCheckout: invocation.Name == "sync",
			AllowSharedCheckout: invocation.AllowSharedCheckout,
			Output:              application.stdout,
		})
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
	if invocation.Name == "create" {
		repository, err := application.discover(ctx, application.cwd)
		if err != nil {
			return 1, err
		}
		if application.create == nil {
			return 1, errors.New("create command is not configured")
		}
		_, err = application.create(ctx, CreateCommandInput{
			Repository: repository,
			CWD:        application.cwd,
			Branch:     invocation.Branch,
			Path:       invocation.Path,
			From:       invocation.From,
			Fetch:      invocation.Fetch,
			Detach:     invocation.Detach,
			JSON:       invocation.JSON,
			Output:     application.stdout,
		})
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
	if invocation.Name == "acquire" {
		repository, err := application.discover(ctx, application.cwd)
		if err != nil {
			return 1, err
		}
		if application.acquire == nil {
			return 1, errors.New("acquire command is not configured")
		}
		result, err := application.acquire(ctx, repository, AcquireInput{
			Branch: invocation.Branch,
			From:   invocation.From,
			Fetch:  invocation.Fetch,
			TTL:    invocation.TTL,
			Owner:  invocation.Owner,
			Ports:  invocation.Ports,
			JSON:   invocation.JSON,
			Now:    application.now(),
		})
		if err != nil {
			return 1, err
		}
		if _, err := io.WriteString(application.stdout, result.Output); err != nil {
			return 1, fmt.Errorf("write acquire result: %w", err)
		}
		return 0, nil
	}
	if invocation.Name == "release" {
		repository, err := application.discover(ctx, application.cwd)
		if err != nil {
			return 1, err
		}
		if application.release == nil {
			return 1, errors.New("release command is not configured")
		}
		result, err := application.release(ctx, ReleaseInput{
			Repository:   repository,
			AssignmentID: invocation.AssignmentID,
			Force:        invocation.Force,
			JSON:         invocation.JSON,
		})
		if err != nil {
			return 1, err
		}
		if _, err := io.WriteString(application.stdout, result.Output); err != nil {
			return 1, fmt.Errorf("write release result: %w", err)
		}
		return 0, nil
	}
	if invocation.Name == "remove" {
		repository, err := application.discover(ctx, application.cwd)
		if err != nil {
			return 1, err
		}
		if application.remove == nil {
			return 1, errors.New("remove command is not configured")
		}
		if err := application.remove(ctx, RemoveInput{
			Repository: repository,
			CWD:        application.cwd,
			Path:       invocation.Path,
			Force:      invocation.Force,
		}); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if invocation.Name == "renew" {
		repository, err := application.discover(ctx, application.cwd)
		if err != nil {
			return 1, err
		}
		result, err := Renew(ctx, RenewInput{
			AssignmentID: invocation.AssignmentID,
			TTL:          invocation.TTL,
			JSON:         invocation.JSON,
			Now:          application.now(),
		}, func(ctx context.Context, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
			return application.renew(ctx, repository, assignmentID, expiresAt)
		})
		if err != nil {
			return 1, err
		}
		if _, err := io.WriteString(application.stdout, result.Output); err != nil {
			return 1, fmt.Errorf("write renew result: %w", err)
		}
		return 0, nil
	}
	if invocation.Name == "list" || invocation.Name == "status" || invocation.Name == "stats" {
		repository, err := application.discover(ctx, application.cwd)
		if err != nil {
			return 1, err
		}
		var output string
		switch invocation.Name {
		case "list":
			records, err := application.queries.HandleList(ctx, repository, application.now())
			if err != nil {
				return 1, err
			}
			output, err = FormatList(records, invocation.JSON)
			if err != nil {
				return 1, err
			}
		case "status":
			record, err := application.queries.HandleStatus(ctx, repository, application.now())
			if err != nil {
				return 1, err
			}
			output, err = FormatStatus(record, invocation.JSON, invocation.Explain)
			if err != nil {
				return 1, err
			}
		case "stats":
			record, err := application.queries.HandleStats(ctx, repository, invocation.Disk)
			if err != nil {
				return 1, err
			}
			output, err = FormatStats(record, invocation.JSON)
			if err != nil {
				return 1, err
			}
		}
		if _, err := io.WriteString(application.stdout, output); err != nil {
			return 1, fmt.Errorf("write %s result: %w", invocation.Name, err)
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
