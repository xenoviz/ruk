package git_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	rukgit "github.com/xenoviz/ruk/internal/git"
)

func TestListWorktreesParsesPorcelainRecords(t *testing.T) {
	tests := []struct {
		name   string
		cwd    string
		stdout string
		want   []rukgit.WorktreeRecord
	}{
		{
			name: "branch detached and locked records",
			cwd:  filepath.Join("repo", "checkout"),
			stdout: "worktree ../primary\x00HEAD abc123\x00branch refs/heads/main\x00\x00" +
				"worktree linked\x00HEAD def456\x00detached\x00\x00" +
				"worktree ../locked\x00HEAD ghi789\x00branch refs/heads/held\x00locked ruk pool\x00\x00",
			want: []rukgit.WorktreeRecord{
				{Path: filepath.Join("..", "primary"), Branch: "main", Head: "abc123"},
				{Path: filepath.Join("linked"), Branch: "(detached)", Head: "def456"},
				{Path: filepath.Join("..", "locked"), Branch: "held", Head: "ghi789"},
			},
		},
		{
			name:   "missing branch defaults detached",
			cwd:    "repo",
			stdout: "worktree child\x00HEAD abc\x00\x00",
			want:   []rukgit.WorktreeRecord{{Path: "child", Branch: "(detached)", Head: "abc"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, err := filepath.Abs(test.cwd)
			if err != nil {
				t.Fatalf("resolve test cwd: %v", err)
			}
			runner := &fakeRunner{results: map[string]rukgit.CommandResult{
				"worktree list --porcelain -z": {Stdout: test.stdout},
			}}
			got, err := rukgit.NewClient(runner.call).ListWorktrees(context.Background(), test.cwd)
			if err != nil {
				t.Fatalf("ListWorktrees returned an error: %v", err)
			}
			want := append([]rukgit.WorktreeRecord(nil), test.want...)
			for index := range want {
				want[index].Path = filepath.Join(base, want[index].Path)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("records = %#v, want %#v", got, want)
			}
		})
	}
}

func TestListRepositoryFilesNormalizesDeduplicatesAndSorts(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   []string
	}{
		{
			name:   "normalizes duplicates and sort order",
			stdout: "z.txt\x00dir\\file.txt\x00a.txt\x00dir/file.txt\x00z.txt\x00",
			want:   []string{"a.txt", "dir/file.txt", "z.txt"},
		},
		{
			name:   "empty listing",
			stdout: "",
			want:   []string{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]rukgit.CommandResult{
				"ls-files -z --cached --others --exclude-standard": {Stdout: test.stdout},
			}}
			got, err := rukgit.NewClient(runner.call).ListRepositoryFiles(context.Background(), ".")
			if err != nil {
				t.Fatalf("ListRepositoryFiles returned an error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("files = %#v, want %#v", got, test.want)
			}
		})
	}
}
