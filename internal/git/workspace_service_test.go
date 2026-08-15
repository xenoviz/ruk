package git_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	rukgit "github.com/xenoviz/ruk/internal/git"
)

type workspaceServiceFS struct {
	paths map[string]string
}

func (filesystem workspaceServiceFS) EvalSymlinks(path string) (string, error) {
	if value, ok := filesystem.paths[path]; ok {
		return value, nil
	}
	return "", os.ErrNotExist
}

func newWorkspaceService(t *testing.T, runner *fakeRunner) (*rukgit.WorkspaceService, string, string) {
	t.Helper()
	container := t.TempDir()
	repositoryRoot := filepath.Join(container, "checkout")
	managedRoot := filepath.Join(container, "pool")
	filesystem := workspaceServiceFS{paths: map[string]string{
		repositoryRoot: repositoryRoot,
		managedRoot:    managedRoot,
	}}
	service, err := rukgit.NewWorkspaceService(rukgit.WorkspaceServiceOptions{
		RepositoryRoot: repositoryRoot,
		ManagedRoot:    managedRoot,
		Runner:         runner.call,
		Files:          filesystem,
	})
	if err != nil {
		t.Fatalf("NewWorkspaceService returned an error: %v", err)
	}
	return service, repositoryRoot, managedRoot
}

func TestWorkspaceServiceCreateUsesGitAddWithinManagedRoot(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"show-ref --verify --quiet refs/heads/agent/task": {ExitCode: 1},
	}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	if service.RepositoryRoot() == service.ManagedRoot() {
		t.Fatalf("repository and managed roots were conflated: %q", service.RepositoryRoot())
	}
	destination := filepath.Join(managedRoot, "task")
	runner.results["worktree add -b agent/task "+destination+" origin/main"] = rukgit.CommandResult{}

	if err := service.Create(context.Background(), destination, "agent/task", "origin/main", false); err != nil {
		t.Fatalf("Create returned an error: %v", err)
	}
	want := [][]string{
		{"show-ref", "--verify", "--quiet", "refs/heads/agent/task"},
		{"worktree", "add", "-b", "agent/task", destination, "origin/main"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestWorkspaceServiceAcceptsNewChildBelowCanonicalPool(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	destination := filepath.Join(managedRoot, "new", "task")
	runner.results["worktree add --detach "+destination+" HEAD"] = rukgit.CommandResult{}

	if err := service.Create(context.Background(), destination, "", "", true); err != nil {
		t.Fatalf("Create rejected a new child below the pool: %v", err)
	}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, []string{"worktree", "add", "--detach", destination, "HEAD"}) {
		t.Fatalf("last call = %#v, want detached add", got)
	}
}

func TestWorkspaceServiceRejectsSymlinkedAncestorOutsidePool(t *testing.T) {
	container := t.TempDir()
	repositoryRoot := filepath.Join(container, "checkout")
	managedRoot := filepath.Join(container, "pool")
	outsideRoot := filepath.Join(container, "outside")
	link := filepath.Join(managedRoot, "linked")
	destination := filepath.Join(link, "task")
	filesystem := workspaceServiceFS{paths: map[string]string{
		repositoryRoot: repositoryRoot,
		managedRoot:    managedRoot,
		link:           outsideRoot,
	}}
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{}}
	service, err := rukgit.NewWorkspaceService(rukgit.WorkspaceServiceOptions{
		RepositoryRoot: repositoryRoot,
		ManagedRoot:    managedRoot,
		Runner:         runner.call,
		Files:          filesystem,
	})
	if err != nil {
		t.Fatalf("NewWorkspaceService returned an error: %v", err)
	}

	err = service.Create(context.Background(), destination, "", "", true)
	if err == nil || !strings.Contains(err.Error(), "outside managed repository root") {
		t.Fatalf("Create error = %v, want symlink escape rejection", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %#v, want no Git command", runner.calls)
	}
}

func TestWorkspaceServiceReturnForceResetsDirtyWorktree(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	destination := filepath.Join(managedRoot, "task")
	runner.results["reset --hard HEAD"] = rukgit.CommandResult{}
	runner.results["clean -ffdx"] = rukgit.CommandResult{}
	runner.results["switch --detach"] = rukgit.CommandResult{}

	if err := service.Return(context.Background(), destination, true, nil); err != nil {
		t.Fatalf("Return returned an error: %v", err)
	}
	want := [][]string{
		{"reset", "--hard", "HEAD"},
		{"clean", "-ffdx"},
		{"switch", "--detach"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestWorkspaceServicePreservesGitCommandFailure(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"show-ref --verify --quiet refs/heads/agent/task": {ExitCode: 1},
	}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	destination := filepath.Join(managedRoot, "task")
	runner.results["worktree add -b agent/task "+destination+" origin/main"] = rukgit.CommandResult{ExitCode: 1, Stderr: "fatal: refused"}

	err := service.Create(context.Background(), destination, "agent/task", "origin/main", false)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("Create error = %v, want Git failure", err)
	}
}

func TestWorkspaceServiceRejectsUnsafePathBeforeRunningGit(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	outside := filepath.Join(filepath.Dir(managedRoot), "outside")

	err := service.Remove(context.Background(), outside, true)
	if err == nil || !strings.Contains(err.Error(), "outside managed repository root") {
		t.Fatalf("Remove error = %v, want unsafe path rejection", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("calls = %#v, want no Git command", runner.calls)
	}
}

func TestWorkspaceServiceRelocksAfterFailedRemoval(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	destination := filepath.Join(managedRoot, "task")
	runner.results["worktree unlock "+destination] = rukgit.CommandResult{}
	runner.results["worktree remove --force "+destination] = rukgit.CommandResult{ExitCode: 1, Stderr: "fatal: busy"}
	runner.results["worktree lock --reason ruk pool "+destination] = rukgit.CommandResult{}

	err := service.SafeRemove(context.Background(), destination, true)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("SafeRemove error = %v, want removal failure", err)
	}
	want := [][]string{
		{"worktree", "unlock", destination},
		{"worktree", "remove", "--force", destination},
		{"worktree", "lock", "--reason", "ruk pool", destination},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want unlock/remove/relock %#v", runner.calls, want)
	}
}

func TestWorkspaceServiceRemoveAndPruneSucceeds(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	destination := filepath.Join(managedRoot, "task")
	runner.results["worktree unlock "+destination] = rukgit.CommandResult{ExitCode: 1, Stderr: "fatal: not locked"}
	runner.results["worktree remove "+destination] = rukgit.CommandResult{}
	runner.results["worktree prune"] = rukgit.CommandResult{}

	if err := service.RemoveAndPrune(context.Background(), destination, false); err != nil {
		t.Fatalf("RemoveAndPrune returned an error: %v", err)
	}
	want := [][]string{
		{"worktree", "unlock", destination},
		{"worktree", "remove", destination},
		{"worktree", "prune"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestWorkspaceServiceRelockFailureIsPreserved(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{}}
	service, _, managedRoot := newWorkspaceService(t, runner)
	destination := filepath.Join(managedRoot, "task")
	runner.results["worktree unlock "+destination] = rukgit.CommandResult{}
	runner.results["worktree remove --force "+destination] = rukgit.CommandResult{ExitCode: 1, Stderr: "fatal: busy"}
	runner.results["worktree lock --reason ruk pool "+destination] = rukgit.CommandResult{ExitCode: 1, Stderr: "fatal: permission denied"}

	err := service.SafeRemove(context.Background(), destination, true)
	if err == nil || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("SafeRemove error = %v, want both failures", err)
	}
}
