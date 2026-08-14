// Package cli composes Ruk commands and their input and output contracts.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Options configures an Application.
type Options struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
}

// Application executes Ruk commands.
type Application struct {
	version string
	stdout  io.Writer
	stderr  io.Writer
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
	return &Application{
		version: options.Version,
		stdout:  stdout,
		stderr:  stderr,
	}
}

// Run executes one Ruk command.
func (application *Application) Run(ctx context.Context, args []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 1, fmt.Errorf("run command: %w", err)
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v") {
		if _, err := fmt.Fprintln(application.stdout, application.version); err != nil {
			return 1, fmt.Errorf("write version: %w", err)
		}
		return 0, nil
	}
	return 1, errors.New("command is not implemented")
}
