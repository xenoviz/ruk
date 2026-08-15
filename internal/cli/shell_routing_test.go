package cli_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
)

func TestApplicationRoutesShellWithStdioAndValidatedAcquireInput(t *testing.T) {
	now := time.Date(2026, 8, 16, 10, 30, 0, 0, time.UTC)
	stdin := bytes.NewBufferString("interactive input")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	discoveries := 0
	var received cli.ShellRouteInput
	application := cli.New(cli.Options{
		CWD:    "/repo",
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Now:    func() time.Time { return now },
		DiscoverRepository: func(context.Context, string) (git.Repository, error) {
			discoveries++
			return git.Repository{Root: "/repo", CommonDir: "/repo/.git"}, nil
		},
		Shell: func(_ context.Context, input cli.ShellRouteInput) (cli.ShellResult, error) {
			received = input
			return cli.ShellResult{ExitCode: 17, Released: true}, nil
		},
	})

	code, err := application.Run(context.Background(), []string{
		"shell", "agent/interactive", "--from", "origin/main", "--fetch",
		"--ttl", "45", "--owner", "agent-7", "--port", "web",
	})
	if err != nil {
		t.Fatalf("Run(shell) error = %v", err)
	}
	if code != 17 {
		t.Fatalf("Run(shell) code = %d, want 17", code)
	}
	if discoveries != 1 {
		t.Fatalf("repository discoveries = %d, want one", discoveries)
	}
	if received.Repository.Root != "/repo" || received.CWD != "/repo" || received.Branch != "agent/interactive" || received.From != "origin/main" || !received.Fetch || received.TTL != "45" || received.Owner != "agent-7" {
		t.Fatalf("shell route input = %#v", received)
	}
	if len(received.Ports) != 1 || received.Ports[0] != "web" || !received.Now.Equal(now) {
		t.Fatalf("shell route ports/time = %#v / %s", received.Ports, received.Now)
	}
	if received.Stdin != io.Reader(stdin) || received.Stdout != io.Writer(stdout) || received.Stderr != io.Writer(stderr) {
		t.Fatal("shell route did not preserve application stdio")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("shell router emitted output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
