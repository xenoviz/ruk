package cli

import (
	"context"
	"errors"

	"github.com/xenoviz/ruk/internal/git"
)

// runInitSync executes init and sync after repository discovery.
func (application *Application) runInitSync(ctx context.Context, invocation Invocation) (int, error) {
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
		Stdin:               application.stdin,
		Stdout:              application.stdout,
		Stderr:              application.stderr,
	})
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// runCreate executes create with the discovered repository.
func (application *Application) runCreate(ctx context.Context, invocation Invocation) (int, error) {
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

// runRun executes run with one discovered repository.
func (application *Application) runRun(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if application.run == nil {
		return 1, errors.New("run command is not configured")
	}
	exitCode, err := application.run(ctx, RunRouteInput{
		Repository:          repository,
		CWD:                 application.cwd,
		Command:             append([]string(nil), invocation.Command...),
		AllowSharedCheckout: invocation.AllowSharedCheckout,
		Stderr:              application.stderr,
		Now:                 application.now(),
	})
	return exitCode, err
}

// runExec executes exec; a branchless exec is the explicit current-workspace
// form and stays on the run route so it cannot accidentally turn an empty
// branch into a new managed acquisition.
func (application *Application) runExec(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if invocation.Branch == "" {
		return application.runRun(ctx, invocation, repository)
	}
	if application.exec == nil {
		return 1, errors.New("exec command is not configured")
	}
	now := application.now()
	exitCode, err := application.exec(ctx, ExecRouteInput{
		Repository: repository,
		CWD:        application.cwd,
		Acquire: AcquireInput{
			Branch: invocation.Branch,
			From:   invocation.From,
			Fetch:  invocation.Fetch,
			TTL:    invocation.TTL,
			Owner:  invocation.Owner,
			Ports:  append([]string(nil), invocation.Ports...),
			JSON:   invocation.JSON,
			Now:    now,
		},
		Command:             append([]string(nil), invocation.Command...),
		AllowSharedCheckout: invocation.AllowSharedCheckout,
		Now:                 now,
	})
	return exitCode, err
}

// runShell executes one interactive managed shell.
func (application *Application) runShell(ctx context.Context, invocation Invocation, repository git.Repository) (int, error) {
	if application.shell == nil {
		return 1, errors.New("shell command is not configured")
	}
	result, err := application.shell(ctx, ShellRouteInput{
		Repository: repository,
		CWD:        application.cwd,
		Branch:     invocation.Branch,
		From:       invocation.From,
		Fetch:      invocation.Fetch,
		TTL:        invocation.TTL,
		Owner:      invocation.Owner,
		Ports:      append([]string(nil), invocation.Ports...),
		Now:        application.now(),
		Stdin:      application.stdin,
		Stdout:     application.stdout,
		Stderr:     application.stderr,
	})
	if err != nil {
		return 1, err
	}
	return result.ExitCode, nil
}
