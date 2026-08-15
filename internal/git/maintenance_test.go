package git_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	rukgit "github.com/xenoviz/ruk/internal/git"
)

func TestAssignWorktreeSwitchesOrCreatesTheRequestedBranch(t *testing.T) {
	tests := []struct {
		name       string
		branchCode int
		want       []string
	}{
		{name: "existing", want: []string{"switch", "agent/task"}},
		{name: "missing", branchCode: 1, want: []string{"switch", "-c", "agent/task", "origin/main"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]rukgit.CommandResult{
				"show-ref --verify --quiet refs/heads/agent/task": {ExitCode: test.branchCode},
				joinArgs(test.want): {},
			}}
			if err := rukgit.NewClient(runner.call).AssignWorktree(context.Background(), ".", "C:/pool/task", "agent/task", "origin/main"); err != nil {
				t.Fatalf("AssignWorktree returned an error: %v", err)
			}
			if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("last call = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestWorktreePoolLockLifecycleUsesExactGitCommands(t *testing.T) {
	destination := "C:/pool/task"
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"worktree lock --reason ruk pool " + destination: {},
		"worktree unlock " + destination:                 {ExitCode: 1, Stderr: "fatal: worktree is not locked"},
		"worktree remove --force " + destination:         {},
	}}
	client := rukgit.NewClient(runner.call)
	if err := client.LockWorktree(context.Background(), ".", destination); err != nil {
		t.Fatalf("LockWorktree returned an error: %v", err)
	}
	if err := client.UnlockWorktree(context.Background(), ".", destination); err != nil {
		t.Fatalf("UnlockWorktree rejected an already-unlocked worktree: %v", err)
	}
	if err := client.RemoveWorktree(context.Background(), ".", destination, true); err != nil {
		t.Fatalf("RemoveWorktree returned an error: %v", err)
	}
}

func TestUnlockWorktreePreservesUnexpectedGitFailure(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"worktree unlock C:/pool/task": {ExitCode: 1, Stderr: "fatal: permission denied"},
	}}
	err := rukgit.NewClient(runner.call).UnlockWorktree(context.Background(), ".", "C:/pool/task")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("UnlockWorktree error = %v, want Git failure", err)
	}
}
