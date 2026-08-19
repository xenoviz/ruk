package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
	"github.com/xenoviz/ruk/internal/statistics"
)

func TestApplicationRoutesQueryCommandsThroughInjectedDependencies(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	key, err := state.TreeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.State{
		Version: state.CurrentVersion,
		Trees: map[string]state.TreeRecord{key: {
			Path: root, Fingerprint: "fingerprint", ProjectionFingerprint: "projection",
			Mode: "managed-install", Projections: []string{"node_modules"}, Branch: "main",
		}},
		Workspaces: map[string]state.WorkspaceRecord{},
		Metrics:    state.EmptyMetrics(),
	}
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git"), PrimaryRoot: root, PrimaryCheckout: true}
	readCalls := 0
	queries := cli.QueryDependencies{
		ListWorktrees: func(context.Context, string) ([]git.WorktreeRecord, error) {
			return []git.WorktreeRecord{{Path: root, Branch: "main", Head: "abc"}}, nil
		},
		ReadState: func(context.Context, string) (state.State, error) {
			readCalls++
			return snapshot, nil
		},
		CurrentFingerprint:  func(context.Context, string) (string, error) { return "fingerprint", nil },
		DependenciesPresent: func(context.Context, string, []string) (bool, error) { return true, nil },
		ProjectionsValid:    func(context.Context, string, state.TreeRecord) (bool, error) { return true, nil },
		MeasureDisk: func(context.Context, state.State) (statistics.DiskStatistics, error) {
			return statistics.DiskStatistics{EstimatedBytesAvoided: 42}, nil
		},
	}
	discoverCalls := 0
	discover := func(_ context.Context, cwd string) (git.Repository, error) {
		discoverCalls++
		if cwd != root {
			t.Fatalf("discovery cwd = %q, want %q", cwd, root)
		}
		return repository, nil
	}

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "list JSON", args: []string{"list", "--json"}, want: `"status":"prepared"`},
		{name: "status human", args: []string{"status"}, want: "Status:      ready\n"},
		{name: "stats disk JSON", args: []string{"stats", "--disk", "--json"}, want: `"estimatedBytesAvoided":42`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			application := cli.New(cli.Options{
				Version: "0.3.0-test", CWD: root, Stdout: &stdout,
				DiscoverRepository: discover, Queries: queries,
				Now: func() time.Time { return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC) },
			})
			code, err := application.Run(context.Background(), test.args)
			if err != nil || code != 0 {
				t.Fatalf("Run = %d, %v", code, err)
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("stdout = %q, want substring %q", stdout.String(), test.want)
			}
		})
	}
	if discoverCalls != 3 || readCalls != 3 {
		t.Fatalf("discover calls = %d, state reads = %d", discoverCalls, readCalls)
	}
}
