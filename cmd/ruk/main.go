package main

import (
	"context"
	"io"
	"os"

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
	defaults, err := cli.NewRuntimeDefaults(cli.RuntimeDefaultsOptions{})
	if err != nil {
		_ = cli.WriteError(stderr, err, cli.JSONRequested(args))
		return 1
	}
	options := defaults.Options()
	options.Version = version
	options.Distribution = updatepkg.Distribution(distribution)
	options.Entrypoint = os.Args[0]
	if executable, executableErr := os.Executable(); executableErr == nil {
		options.Entrypoint = executable
	}
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
