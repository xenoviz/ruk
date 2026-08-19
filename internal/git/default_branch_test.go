package git_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	rukgit "github.com/xenoviz/ruk/internal/git"
)

func TestFetchDefaultBranchResolvesRemoteHeadBeforeFetching(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"remote":                         {Stdout: "origin\nbackup\n"},
		"ls-remote --symref origin HEAD": {Stdout: "ref: refs/heads/trunk\tHEAD\n0123456789\tHEAD\n"},
		"fetch --prune origin":           {},
	}}
	ref, err := rukgit.NewClient(runner.call).FetchDefaultBranch(context.Background(), ".")
	if err != nil {
		t.Fatalf("FetchDefaultBranch returned an error: %v", err)
	}
	if ref != "refs/remotes/origin/trunk" {
		t.Fatalf("ref = %q, want origin/trunk tracking ref", ref)
	}
	want := [][]string{
		{"remote"},
		{"ls-remote", "--symref", "origin", "HEAD"},
		{"fetch", "--prune", "origin"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestFetchDefaultBranchRejectsRemoteWithoutSymbolicHead(t *testing.T) {
	runner := &fakeRunner{results: map[string]rukgit.CommandResult{
		"remote":                           {Stdout: "upstream\n"},
		"ls-remote --symref upstream HEAD": {Stdout: "0123456789\tHEAD\n"},
	}}
	_, err := rukgit.NewClient(runner.call).FetchDefaultBranch(context.Background(), ".")
	if err == nil || !strings.Contains(err.Error(), "does not advertise a default branch") {
		t.Fatalf("error = %v, want missing default branch", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %#v, want no fetch after invalid HEAD", runner.calls)
	}
}
