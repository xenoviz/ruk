package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	updatepkg "github.com/xenoviz/ruk/internal/update"
)

// runWorktreesAll executes the host-wide worktrees --all view, which does not
// discover a repository.
func (application *Application) runWorktreesAll(ctx context.Context, invocation Invocation) (int, error) {
	record, err := application.queries.HandleAllWorktrees(ctx)
	if err != nil {
		return 1, err
	}
	output, err := FormatAllWorktrees(record, invocation.JSON)
	if err != nil {
		return 1, err
	}
	if _, err := io.WriteString(application.stdout, output); err != nil {
		return 1, fmt.Errorf("write worktrees result: %w", err)
	}
	return 0, nil
}

// runUpdate executes one explicit self-update operation.
func (application *Application) runUpdate(ctx context.Context, invocation Invocation) (int, error) {
	result, err := application.update(ctx, updatepkg.Options{
		Distribution:    application.distribution,
		CurrentVersion:  application.version,
		CheckOnly:       invocation.Check,
		Entrypoint:      application.entrypoint,
		Stdin:           application.stdin,
		Stdout:          application.stdout,
		Stderr:          application.stderr,
		MachineReadable: invocation.JSON,
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

// runQueries executes the repository-scoped read commands: list, status,
// stats, and the per-repository worktrees view.
func (application *Application) runQueries(ctx context.Context, invocation Invocation) (int, error) {
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
	case "worktrees":
		record, err := application.queries.HandleWorktrees(ctx, repository)
		if err != nil {
			return 1, err
		}
		output, err = FormatWorktrees(record, invocation.JSON)
		if err != nil {
			return 1, err
		}
	default:
		return 1, errors.New("command is not implemented")
	}
	if _, err := io.WriteString(application.stdout, output); err != nil {
		return 1, fmt.Errorf("write %s result: %w", invocation.Name, err)
	}
	return 0, nil
}
