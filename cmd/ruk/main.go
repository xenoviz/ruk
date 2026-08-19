package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xenoviz/ruk/internal/cli"
	updatepkg "github.com/xenoviz/ruk/internal/update"
)

// version and distribution are injected by release builds. Distribution must
// remain explicit because package installs delegate updates to their package
// manager while standalone binaries replace themselves.
var (
	version      = "dev"
	distribution = "standalone"
)

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	entrypoint, err := resolveEntrypoint(os.Executable)
	if err != nil {
		_ = cli.WriteError(stderr, err, cli.JSONRequested(args))
		return 1
	}
	defaults, err := cli.NewRuntimeDefaults(cli.RuntimeDefaultsOptions{})
	if err != nil {
		_ = cli.WriteError(stderr, err, cli.JSONRequested(args))
		return 1
	}
	options := defaults.Options()
	options.Version = version
	options.Distribution = updatepkg.Distribution(distribution)
	options.Entrypoint = entrypoint
	options.Stdout = stdout
	options.Stderr = stderr
	application := cli.New(options)
	code, err := application.Run(context.Background(), args)
	if err != nil {
		_ = cli.WriteError(stderr, err, cli.JSONRequested(args))
		return 1
	}
	return code
}

// resolveEntrypoint resolves the executable target before update ownership is
// selected. Package launchers may invoke the native binary through a symlink;
// the distribution marker is stored beside the target, not beside the link.
// Failure is fatal because falling back to an unresolved path could select the
// wrong package manager or update the wrong executable.
func resolveEntrypoint(executable func() (string, error)) (string, error) {
	path, err := executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlinks: %w", err)
	}
	abs, err := filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	return abs, nil
}
