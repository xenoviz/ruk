package cli_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
)

func TestApplicationRoutesWarmAndWritesRenderedOutputOnce(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	var stdout bytes.Buffer
	discoveries := 0
	var received cli.WarmRequest
	application := cli.New(cli.Options{
		CWD:    root,
		Stdout: &stdout,
		DiscoverRepository: func(_ context.Context, cwd string) (git.Repository, error) {
			discoveries++
			if cwd != root {
				t.Fatalf("discovery cwd = %q, want %q", cwd, root)
			}
			return repository, nil
		},
		Warm: func(_ context.Context, got git.Repository, input cli.WarmRequest) (lifecycle.WarmResult, error) {
			if got != repository {
				t.Fatalf("warm repository = %#v, want %#v", got, repository)
			}
			received = input
			return lifecycle.WarmResult{Status: "warmed", Requested: 2, Available: 3, Created: []string{"/pool/one"}}, nil
		},
	})
	code, err := application.Run(context.Background(), []string{"warm", "--count", "2", "--from", "origin/main", "--fetch"})
	if err != nil || code != 0 {
		t.Fatalf("warm route = code %d, error %v", code, err)
	}
	if discoveries != 1 || stdout.String() != "Available workspaces: 3 (1 created)\n" {
		t.Fatalf("discoveries=%d stdout=%q, want one rendered result", discoveries, stdout.String())
	}
	if received.Count != 2 || received.From != "origin/main" || !received.Fetch {
		t.Fatalf("warm input = %#v", received)
	}
}

func TestApplicationRoutesGCPassesRepositoryAndNowAndWritesOutputOnce(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	var stdout bytes.Buffer
	discoveries := 0
	var received cli.GCRequest
	application := cli.New(cli.Options{
		CWD: root, Stdout: &stdout, Now: func() time.Time { return now },
		DiscoverRepository: func(_ context.Context, _ string) (git.Repository, error) {
			discoveries++
			return repository, nil
		},
		GC: func(_ context.Context, got git.Repository, input cli.GCRequest) (lifecycle.GCResult, error) {
			if got != repository {
				t.Fatalf("gc repository = %#v, want %#v", got, repository)
			}
			received = input
			return lifecycle.GCResult{Status: "collected", Removed: []lifecycle.GCRemovedRecord{{Path: "/pool/old", Lifecycle: "available", Reason: "older"}}, Expired: []lifecycle.GCExpiredRecord{}}, nil
		},
	})
	code, err := application.Run(context.Background(), []string{"gc", "--max-age", "30", "--apply", "--force-expired"})
	if err != nil || code != 0 {
		t.Fatalf("gc route = code %d, error %v", code, err)
	}
	if discoveries != 1 || stdout.String() != "Collected: 1 workspace(s)\n" {
		t.Fatalf("discoveries=%d stdout=%q, want one rendered result", discoveries, stdout.String())
	}
	if !received.Options.Apply || !received.Options.ForceExpired || !received.Options.Now.Equal(now) || !received.Options.OlderThan.Equal(now.Add(-30*time.Minute)) || received.Options.CurrentWorkspacePath != root {
		t.Fatalf("gc request = %#v", received)
	}
}

func TestApplicationRoutesRunWithValidatedInputsAndNoSuccessOutput(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	var stdout bytes.Buffer
	discoveries := 0
	var received cli.RunRouteInput
	application := cli.New(cli.Options{
		CWD: root, Stdout: &stdout, Now: func() time.Time { return now },
		DiscoverRepository: func(_ context.Context, _ string) (git.Repository, error) {
			discoveries++
			return repository, nil
		},
		Run: func(_ context.Context, input cli.RunRouteInput) (int, error) {
			received = input
			return 23, nil
		},
	})
	code, err := application.Run(context.Background(), []string{"run", "--allow-shared-checkout", "--", "tool", "--flag"})
	if err != nil || code != 23 {
		t.Fatalf("run route = code %d, error %v", code, err)
	}
	if discoveries != 1 || stdout.Len() != 0 {
		t.Fatalf("discoveries=%d stdout=%q, want one discovery and no output", discoveries, stdout.String())
	}
	if received.Repository != repository || received.CWD != root || !received.AllowSharedCheckout || received.Now != now || strings.Join(received.Command, " ") != "tool --flag" {
		t.Fatalf("run input = %#v", received)
	}
}

func TestApplicationRoutesExecWithAcquireOptionsCommandAndExitCode(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	wantErr := errors.New("child exited unsuccessfully")
	var stdout bytes.Buffer
	discoveries := 0
	var received cli.ExecRouteInput
	application := cli.New(cli.Options{
		CWD: root, Stdout: &stdout, Now: func() time.Time { return now },
		DiscoverRepository: func(_ context.Context, _ string) (git.Repository, error) {
			discoveries++
			return repository, nil
		},
		Exec: func(_ context.Context, input cli.ExecRouteInput) (int, error) {
			received = input
			return 17, wantErr
		},
	})
	code, err := application.Run(context.Background(), []string{
		"exec", "feature", "--from", "origin/main", "--fetch", "--ttl", "5", "--owner", "owner", "--port", "api", "--", "tool", "--flag",
	})
	if !errors.Is(err, wantErr) || code != 17 {
		t.Fatalf("exec route = code %d, error %v", code, err)
	}
	if discoveries != 1 || stdout.Len() != 0 {
		t.Fatalf("discoveries=%d stdout=%q, want one discovery and no output", discoveries, stdout.String())
	}
	if received.Repository != repository || received.CWD != root || received.AllowSharedCheckout || received.Now != now {
		t.Fatalf("exec repository/context = %#v", received)
	}
	if received.Acquire.Branch != "feature" || received.Acquire.From != "origin/main" || !received.Acquire.Fetch || received.Acquire.TTL != "5" || received.Acquire.Owner != "owner" || len(received.Acquire.Ports) != 1 || received.Acquire.Ports[0] != "api" || received.Acquire.Now != now {
		t.Fatalf("exec acquire input = %#v", received.Acquire)
	}
	if strings.Join(received.Command, " ") != "tool --flag" {
		t.Fatalf("exec command = %#v", received.Command)
	}
}

func TestApplicationRoutesBranchlessExecThroughCurrentWorkspaceRun(t *testing.T) {
	root := t.TempDir()
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	var received cli.RunRouteInput
	application, discoveries := routingApplication(nil, func(options *cli.Options) {
		options.Now = func() time.Time { return now }
		options.CWD = root
		options.DiscoverRepository = func(context.Context, string) (git.Repository, error) { return repository, nil }
		options.Run = func(_ context.Context, input cli.RunRouteInput) (int, error) {
			received = input
			return 23, nil
		}
		options.Exec = func(context.Context, cli.ExecRouteInput) (int, error) {
			return 1, errors.New("branchless exec must not acquire")
		}
	})
	code, err := application.Run(context.Background(), []string{"exec", "--", "tool", "--flag"})
	if err != nil || code != 23 {
		t.Fatalf("branchless exec = code %d, error %v", code, err)
	}
	if *discoveries != 1 || received.Repository != repository || received.CWD != root || received.Now != now || strings.Join(received.Command, " ") != "tool --flag" {
		t.Fatalf("current-workspace run input = %#v discoveries=%d", received, *discoveries)
	}
}

func TestApplicationRoutesReturnDiscoveryErrorsWithoutCallingOperations(t *testing.T) {
	wantErr := errors.New("discovery failed")
	discoveries := 0
	called := false
	application := cli.New(cli.Options{
		CWD: "/repo",
		DiscoverRepository: func(context.Context, string) (git.Repository, error) {
			discoveries++
			return git.Repository{}, wantErr
		},
		Warm: func(context.Context, git.Repository, cli.WarmRequest) (lifecycle.WarmResult, error) {
			called = true
			return lifecycle.WarmResult{}, nil
		},
	})
	code, err := application.Run(context.Background(), []string{"warm", "--count", "1"})
	if !errors.Is(err, wantErr) || code != 1 || discoveries != 1 || called {
		t.Fatalf("discovery failure = code %d error %v discoveries=%d called=%v", code, err, discoveries, called)
	}
}
