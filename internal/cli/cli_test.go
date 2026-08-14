package cli_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
)

func TestVersionReportsConfiguredVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	application := cli.New(cli.Options{
		Version: "0.3.0-test",
		Stdout:  &stdout,
		Stderr:  &stderr,
	})

	code, err := application.Run(context.Background(), []string{"--version"})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if code != 0 {
		t.Fatalf("Run returned exit code %d, want 0", code)
	}
	if got := stdout.String(); got != "0.3.0-test\n" {
		t.Fatalf("stdout = %q, want %q", got, "0.3.0-test\n")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty output", got)
	}
}
