package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	updatepkg "github.com/xenoviz/ruk/internal/update"
)

// Application executes Ruk commands.
type Application struct {
	version      string
	distribution updatepkg.Distribution
	entrypoint   string
	stdout       io.Writer
	stderr       io.Writer
	stdin        io.Reader
	update       UpdateOperation
	renew        RepositoryRenewOperation
	sync         SyncRouteOperation
	create       CreateRouteOperation
	acquire      AcquireRouteOperation
	release      ReleaseRouteOperation
	remove       RemoveRouteOperation
	warm         WarmRouteOperation
	gc           GCRouteOperation
	run          RunRouteOperation
	exec         ExecRouteOperation
	shell        ShellRouteOperation
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
	stdin := options.Stdin
	if stdin == nil {
		stdin = os.Stdin
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
		entrypoint:   options.Entrypoint,
		stdout:       stdout,
		stderr:       stderr,
		stdin:        stdin,
		update:       updateOperation,
		renew:        renewOperation,
		sync:         options.Sync,
		create:       options.Create,
		acquire:      options.Acquire,
		release:      options.Release,
		remove:       options.Remove,
		warm:         options.Warm,
		gc:           options.GC,
		run:          options.Run,
		exec:         options.Exec,
		shell:        options.Shell,
		cwd:          cwd,
		discover:     discover,
		queries:      mergeQueryDependencies(options.Queries, defaultQueryDependencies()),
		now:          now,
	}
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

// repositoryHandler executes one command against a discovered repository.
type repositoryHandler func(context.Context, Invocation, git.Repository) (int, error)

// withRepository discovers the current checkout and delegates to handler.
func (application *Application) withRepository(ctx context.Context, invocation Invocation, handler repositoryHandler) (int, error) {
	repository, err := application.discover(ctx, application.cwd)
	if err != nil {
		return 1, err
	}
	return handler(ctx, invocation, repository)
}
