package git_test

import (
	"context"
	"reflect"
	"testing"

	rukgit "github.com/xenoviz/ruk/internal/git"
)

func TestAddWorktreeUsesExistingBranch(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"show-ref --verify --quiet refs/heads/agent/task": {},
		"worktree add C:/pool/task agent/task":            {},
	}}
	client := rukgit.NewClient(runner.call)
	if err := client.AddWorktree(context.Background(), ".", "C:/pool/task", "agent/task", "origin/main", false); err != nil {
		t.Fatalf("AddWorktree returned an error: %v", err)
	}
	want := [][]string{
		{"show-ref", "--verify", "--quiet", "refs/heads/agent/task"},
		{"worktree", "add", "C:/pool/task", "agent/task"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestAddWorktreeCreatesMissingBranchOrDetachedWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		detach bool
		want   []string
	}{
		{name: "branch", want: []string{"worktree", "add", "-b", "agent/task", "C:/pool/task", "origin/main"}},
		{name: "detached", detach: true, want: []string{"worktree", "add", "--detach", "C:/pool/task", "origin/main"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]rukgit.CommandResult{
				"show-ref --verify --quiet refs/heads/agent/task": {ExitCode: 1},
				joinArgs(test.want): {},
			}}
			if err := rukgit.NewClient(runner.call).AddWorktree(context.Background(), ".", "C:/pool/task", "agent/task", "origin/main", test.detach); err != nil {
				t.Fatalf("AddWorktree returned an error: %v", err)
			}
			got := runner.calls[len(runner.calls)-1]
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("last call = %#v, want %#v", got, test.want)
			}
		})
	}
}

func joinArgs(args []string) string {
	result := ""
	for index, arg := range args {
		if index > 0 {
			result += " "
		}
		result += arg
	}
	return result
}
