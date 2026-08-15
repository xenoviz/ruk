package cli_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

func TestApplicationRoutesRenewThroughRepositoryLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	called := 0
	var stdout bytes.Buffer
	application := cli.New(cli.Options{
		Version: "0.3.0-test",
		CWD:     root,
		Stdout:  &stdout,
		Now:     func() time.Time { return now },
		DiscoverRepository: func(_ context.Context, cwd string) (git.Repository, error) {
			if cwd != root {
				t.Fatalf("discovery cwd = %q", cwd)
			}
			return repository, nil
		},
		Renew: func(_ context.Context, gotRepository git.Repository, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
			called++
			if gotRepository != repository || assignmentID != "assignment-1" || !expiresAt.Equal(now.Add(15*time.Minute)) {
				t.Fatalf("renew inputs = %#v, %q, %s", gotRepository, assignmentID, expiresAt)
			}
			return state.WorkspaceRecord{
				Path: filepath.Join(root, "slot"),
				Assignment: &state.AssignmentRecord{
					ID: assignmentID, ExpiresAt: "2026-08-16T12:15:00.000Z",
				},
			}, nil
		},
	})

	code, err := application.Run(context.Background(), []string{"renew", "assignment-1", "--ttl", "15", "--json"})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if called != 1 {
		t.Fatalf("renew calls = %d", called)
	}
	want := `{"status":"renewed","assignmentId":"assignment-1","path":"` + filepath.ToSlash(filepath.Join(root, "slot")) + `","expiresAt":"2026-08-16T12:15:00.000Z"}` + "\n"
	if got := filepath.ToSlash(stdout.String()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
