package git_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/git"
)

type fakeRunner struct {
	results             map[string]git.CommandResult
	calls               [][]string
	contexts            []context.Context
	contextErrors       []error
	contextHasDeadlines []bool
}

func (runner *fakeRunner) call(ctx context.Context, _ string, args []string) (git.CommandResult, error) {
	runner.calls = append(runner.calls, append([]string(nil), args...))
	runner.contexts = append(runner.contexts, ctx)
	runner.contextErrors = append(runner.contextErrors, ctx.Err())
	_, hasDeadline := ctx.Deadline()
	runner.contextHasDeadlines = append(runner.contextHasDeadlines, hasDeadline)
	result, ok := runner.results[strings.Join(args, " ")]
	if !ok {
		return git.CommandResult{ExitCode: 1, Stderr: "unexpected command"}, nil
	}
	return result, nil
}

func TestDiscoverLinkedWorktreeNormalizesPrimaryRoot(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"rev-parse --show-toplevel":                         {Stdout: "../primary/linked\n"},
		"rev-parse --path-format=absolute --git-common-dir": {Stdout: "../primary/.git\n"},
		"rev-parse --path-format=absolute --git-dir":        {Stdout: "../primary/.git/worktrees/linked\n"},
		"worktree list --porcelain -z":                      {Stdout: "worktree ../primary\x00HEAD aaa\x00branch refs/heads/main\x00\x00worktree ../primary/linked\x00HEAD bbb\x00branch refs/heads/agent\x00\x00"},
	}}
	repository, err := git.NewClient(runner.call).Discover(context.Background(), filepath.Join("repo", "linked"))
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	if !strings.HasSuffix(repository.Root, filepath.Join("primary", "linked")) {
		t.Fatalf("Root = %q, want linked checkout", repository.Root)
	}
	if !strings.HasSuffix(repository.CommonDir, filepath.Join("primary", ".git")) {
		t.Fatalf("CommonDir = %q, want common Git directory", repository.CommonDir)
	}
	if !strings.HasSuffix(repository.PrimaryRoot, "primary") || repository.PrimaryCheckout {
		t.Fatalf("primary = (%q, %v), want primary root and linked checkout", repository.PrimaryRoot, repository.PrimaryCheckout)
	}
	want := [][]string{
		{"rev-parse", "--show-toplevel"},
		{"rev-parse", "--path-format=absolute", "--git-common-dir"},
		{"rev-parse", "--path-format=absolute", "--git-dir"},
		{"worktree", "list", "--porcelain", "-z"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestDiscoverSeparateGitDirKeepsPrimaryRootAtRepositoryRoot(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"rev-parse --show-toplevel":                         {Stdout: "../primary\n"},
		"rev-parse --path-format=absolute --git-common-dir": {Stdout: "../shared.git\n"},
		"rev-parse --path-format=absolute --git-dir":        {Stdout: "../shared.git\n"},
		"worktree list --porcelain -z":                      {Stdout: "worktree ../primary\x00HEAD aaa\x00"},
	}}
	repository, err := git.NewClient(runner.call).Discover(context.Background(), filepath.Join("repo", "linked"))
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	if !strings.HasSuffix(repository.PrimaryRoot, "primary") || !repository.PrimaryCheckout {
		t.Fatalf("primary = (%q, %v), want repository root and primary checkout", repository.PrimaryRoot, repository.PrimaryCheckout)
	}
}

func TestDiscoverWorktreePathPreservesNewline(t *testing.T) {
	newlinePath := "../primary/line\nbreak"
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"rev-parse --show-toplevel":                         {Stdout: newlinePath + "\n"},
		"rev-parse --path-format=absolute --git-common-dir": {Stdout: "../primary/.git\n"},
		"rev-parse --path-format=absolute --git-dir":        {Stdout: "../primary/.git/worktrees/line-break\n"},
		"worktree list --porcelain -z":                      {Stdout: "worktree " + newlinePath + "\x00HEAD aaa\x00branch refs/heads/main\x00\x00"},
	}}
	repository, err := git.NewClient(runner.call).Discover(context.Background(), filepath.Join("repo", "linked"))
	if err != nil {
		t.Fatalf("Discover returned an error: %v", err)
	}
	want := filepath.Join("repo", "linked", newlinePath)
	want, err = filepath.Abs(want)
	if err != nil {
		t.Fatal(err)
	}
	if repository.PrimaryRoot != want || !strings.Contains(repository.PrimaryRoot, "line\nbreak") {
		t.Fatalf("primary root = %q, want newline path %q", repository.PrimaryRoot, want)
	}
}

func TestLocalBranchAndRefVerificationUseExactRefs(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"show-ref --verify --quiet refs/heads/agent/test":    {ExitCode: 0},
		"show-ref --verify --quiet refs/remotes/origin/main": {ExitCode: 1},
	}}
	client := git.NewClient(runner.call)
	branch, err := client.LocalBranchExists(context.Background(), ".", "agent/test")
	if err != nil || !branch {
		t.Fatalf("LocalBranchExists = %v, %v; want true, nil", branch, err)
	}
	ref, err := client.RefExists(context.Background(), ".", "refs/remotes/origin/main")
	if err != nil || ref {
		t.Fatalf("RefExists = %v, %v; want false, nil", ref, err)
	}
}

func TestCurrentBranchReportsDetachedCheckout(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"branch --show-current": {Stdout: "\n"},
	}}
	branch, err := git.NewClient(runner.call).CurrentBranch(context.Background(), ".")
	if err != nil || branch != "(detached)" {
		t.Fatalf("CurrentBranch = %q, %v; want (detached), nil", branch, err)
	}
}

func TestSelectRemoteDoesNotMistakeLocalBranchForRemoteShorthand(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"remote": {Stdout: "origin\nupstream\n"},
		"show-ref --verify --quiet refs/heads/upstream/main": {ExitCode: 0},
	}}
	remote, err := git.NewClient(runner.call).SelectRemote(context.Background(), ".", "upstream/main")
	if err != nil || remote != "origin" {
		t.Fatalf("SelectRemote = %q, %v; want origin for local branch start point", remote, err)
	}
}

func TestSelectRemoteValidatesShorthandAndQualifiedStartPoints(t *testing.T) {
	for _, startPoint := range []string{"upstream/main", "refs/remotes/upstream/main"} {
		runner := &fakeRunner{results: map[string]git.CommandResult{
			"remote": {Stdout: "upstream\n"},
		}}
		remote, err := git.NewClient(runner.call).SelectRemote(context.Background(), ".", startPoint)
		if err != nil || remote != "upstream" {
			t.Fatalf("SelectRemote(%q) = %q, %v; want upstream", startPoint, remote, err)
		}
	}
}

func TestSelectRemoteRejectsMissingShorthandAndQualifiedRemotes(t *testing.T) {
	for _, startPoint := range []string{"missing/main", "refs/remotes/missing/main"} {
		runner := &fakeRunner{results: map[string]git.CommandResult{
			"remote": {Stdout: "origin\n"},
			"show-ref --verify --quiet refs/heads/missing/main": {ExitCode: 1},
		}}
		if _, err := git.NewClient(runner.call).SelectRemote(context.Background(), ".", startPoint); err == nil || err.Error() != "Git remote missing does not exist" {
			t.Fatalf("SelectRemote(%q) error = %v, want missing remote", startPoint, err)
		}
	}
}

func TestSelectRemotePrefersOrigin(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"remote": {Stdout: "backup\norigin\n"},
	}}
	remote, err := git.NewClient(runner.call).SelectRemote(context.Background(), ".", "")
	if err != nil || remote != "origin" {
		t.Fatalf("origin SelectRemote = %q, %v; want origin", remote, err)
	}
}

func TestSelectRemoteRejectsMultipleRemotesWithoutOrigin(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"remote": {Stdout: "backup\nupstream\n"},
	}}
	if _, err := git.NewClient(runner.call).SelectRemote(context.Background(), ".", ""); err == nil || err.Error() != "Multiple Git remotes exist; use --from to select one explicitly" {
		t.Fatalf("ambiguous selection error = %v, want ambiguity", err)
	}
}

func TestSelectRemoteRejectsNoRemote(t *testing.T) {
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"remote": {},
	}}
	if _, err := git.NewClient(runner.call).SelectRemote(context.Background(), ".", ""); err == nil || err.Error() != "Git remote does not exist" {
		t.Fatalf("no remote error = %v, want missing remote", err)
	}
}

func TestFetchCommandConstructionAndExecution(t *testing.T) {
	args, err := git.FetchCommand("origin", "refs/remotes/origin/main")
	if err != nil {
		t.Fatalf("FetchCommand returned an error: %v", err)
	}
	if want := []string{"fetch", "--prune", "origin"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("FetchCommand = %#v, want %#v", args, want)
	}
	runner := &fakeRunner{results: map[string]git.CommandResult{
		"fetch --prune origin": {},
	}}
	if err := git.NewClient(runner.call).Fetch(context.Background(), ".", "origin", "refs/remotes/origin/main"); err != nil {
		t.Fatalf("Fetch returned an error: %v", err)
	}
}
