package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	var got cli.RenewRecord
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout = %q is not a renew record: %v", stdout.String(), err)
	}
	want := cli.RenewRecord{
		Status:       "renewed",
		AssignmentID: "assignment-1",
		Path:         filepath.Join(root, "slot"),
		ExpiresAt:    "2026-08-16T12:15:00.000Z",
	}
	if got != want {
		t.Fatalf("stdout record = %#v, want %#v", got, want)
	}
}
