package main

import (
	"context"
	"fmt"
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
	application := cli.New(cli.Options{
		Version:      version,
		Distribution: updatepkg.Distribution(distribution),
		Stdout:       stdout,
		Stderr:       stderr,
	})
	code, err := application.Run(context.Background(), args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ruk: %v\n", err)
		return 1
	}
	return code
}
