package git_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	rukgit "github.com/xenoviz/ruk/internal/git"
)

func TestReturnWorktreeRefusesUncommittedChangesWithoutForce(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"status --porcelain": {Stdout: " M source.go\n"},
	}}
	err := rukgit.NewClient(runner.call).ReturnWorktree(context.Background(), ".", false, nil)
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("ReturnWorktree error = %v, want dirty-worktree refusal", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v, want only status inspection", runner.calls)
	}
}

func TestReturnWorktreeForceResetsCleansWithProjectionExclusionsAndDetaches(t *testing.T) {
	want := [][]string{
		{"reset", "--hard", "HEAD"},
		{"clean", "-ffdx", "-e", "/node_modules/", "-e", "/packages/a\\[b\\]/node_modules/"},
		{"switch", "--detach"},
	}
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		joinArgs(want[0]): {},
		joinArgs(want[1]): {},
		joinArgs(want[2]): {},
	}}
	err := rukgit.NewClient(runner.call).ReturnWorktree(
		context.Background(),
		".",
		true,
		[]string{"/node_modules/", `packages\a[b]\node_modules`},
	)
	if err != nil {
		t.Fatalf("ReturnWorktree returned an error: %v", err)
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestReturnCleanWorktreeSkipsReset(t *testing.T) {
	want := [][]string{
		{"status", "--porcelain"},
		{"clean", "-ffdx"},
		{"switch", "--detach"},
	}
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		joinArgs(want[0]): {},
		joinArgs(want[1]): {},
		joinArgs(want[2]): {},
	}}
	if err := rukgit.NewClient(runner.call).ReturnWorktree(context.Background(), ".", false, nil); err != nil {
		t.Fatalf("ReturnWorktree returned an error: %v", err)
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}
