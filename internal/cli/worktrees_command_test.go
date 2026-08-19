package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

func TestHandleWorktreesSortsByPathAndReportsExistence(t *testing.T) {
	later := filepath.Join(string(filepath.Separator), "z-workspace")
	earlier := filepath.Join(string(filepath.Separator), "a-workspace")
	laterKey, err := state.TreeKey(later)
	if err != nil {
		t.Fatal(err)
	}
	earlierKey, err := state.TreeKey(earlier)
	if err != nil {
		t.Fatal(err)
	}
	queries := cli.QueryDependencies{
		ReadWorktreeRegistry: func(context.Context, string) (state.WorktreeRegistry, error) {
			return state.WorktreeRegistry{
				Version: state.WorktreeRegistryVersion,
				Worktrees: map[string]state.WorktreeRecord{
					laterKey: {
						Path: later, Branch: "agent/later", Source: state.WorktreeSourceWarm,
						CreatedAt: "2026-08-19T11:00:00.000Z", UpdatedAt: "2026-08-19T11:00:00.000Z",
					},
					earlierKey: {
						Path: earlier, Branch: "agent/earlier", Source: state.WorktreeSourceAcquire,
						CreatedAt: "2026-08-19T10:00:00.000Z", UpdatedAt: "2026-08-19T10:30:00.000Z",
					},
				},
			}, nil
		},
		WorktreePathExists: func(path string) bool { return path == earlier },
	}
	got, err := queries.HandleWorktrees(context.Background(), git.Repository{Root: "/repo", CommonDir: "/repo/.git"})
	if err != nil {
		t.Fatalf("HandleWorktrees returned an error: %v", err)
	}
	if got.Repository != "/repo" || got.CommonDir != "/repo/.git" {
		t.Fatalf("repository fields = %#v", got)
	}
	if len(got.Worktrees) != 2 || got.Worktrees[0].Path != earlier || got.Worktrees[1].Path != later {
		t.Fatalf("worktrees were not sorted by path: %#v", got.Worktrees)
	}
	if !got.Worktrees[0].Exists || got.Worktrees[1].Exists {
		t.Fatalf("exists flags = %#v", got.Worktrees)
	}
	if got.Worktrees[0].Source != state.WorktreeSourceAcquire || got.Worktrees[1].Source != state.WorktreeSourceWarm {
		t.Fatalf("sources = %#v", got.Worktrees)
	}
}

func TestHandleWorktreesRequiresDependencies(t *testing.T) {
	repository := git.Repository{Root: "/repo", CommonDir: "/repo/.git"}
	_, err := (cli.QueryDependencies{}).HandleWorktrees(context.Background(), repository)
	if err == nil || !strings.Contains(err.Error(), "worktrees query dependencies are incomplete") {
		t.Fatalf("empty dependencies error = %v", err)
	}
	_, err = (cli.QueryDependencies{
		ReadWorktreeRegistry: func(context.Context, string) (state.WorktreeRegistry, error) {
			return *state.EmptyWorktreeRegistry(), nil
		},
	}).HandleWorktrees(context.Background(), repository)
	if err == nil || !strings.Contains(err.Error(), "worktrees query dependencies are incomplete") {
		t.Fatalf("missing exists predicate error = %v", err)
	}
}

func TestHandleWorktreesConcurrentReads(t *testing.T) {
	queries := cli.QueryDependencies{
		ReadWorktreeRegistry: func(context.Context, string) (state.WorktreeRegistry, error) {
			return *state.EmptyWorktreeRegistry(), nil
		},
		WorktreePathExists: func(string) bool { return false },
	}
	repository := git.Repository{Root: "/repo", CommonDir: "/repo/.git"}
	const readers = 8
	var wait sync.WaitGroup
	failures := make(chan error, readers)
	for range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, err := queries.HandleWorktrees(context.Background(), repository)
			if err != nil {
				failures <- err
				return
			}
			if got.Worktrees == nil {
				failures <- errors.New("worktrees array was nil")
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent HandleWorktrees returned an error: %v", err)
	}
}

func TestFormatWorktreesJSONEmitsExactlyOneValueWithEmptyArray(t *testing.T) {
	encoded, err := cli.FormatWorktrees(cli.WorktreesResponse{
		Repository: "/repo", CommonDir: "/repo/.git", Worktrees: []cli.WorktreesRecord{},
	}, true)
	if err != nil {
		t.Fatalf("FormatWorktrees returned an error: %v", err)
	}
	if !strings.HasSuffix(encoded, "\n") || strings.Count(encoded, "\n") != 1 {
		t.Fatalf("JSON output = %q, want exactly one JSON value ending in a newline", encoded)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("JSON output is not one value: %v", err)
	}
	worktrees, ok := decoded["worktrees"].([]any)
	if !ok || worktrees == nil {
		t.Fatalf("worktrees = %#v, want []", decoded["worktrees"])
	}
	if len(worktrees) != 0 {
		t.Fatalf("worktrees = %#v, want empty array", worktrees)
	}
	if _, exists := decoded["repository"]; !exists {
		t.Fatal("JSON missing repository")
	}
	if _, exists := decoded["commonDir"]; !exists {
		t.Fatal("JSON missing commonDir")
	}
}

func TestFormatWorktreesHumanEmptyAndPopulated(t *testing.T) {
	empty := cli.FormatWorktreesHuman(cli.WorktreesResponse{Worktrees: []cli.WorktreesRecord{}})
	if empty != "No Ruk-created worktrees are tracked for this repository.\n" {
		t.Fatalf("empty human output = %q", empty)
	}
	populated := cli.FormatWorktreesHuman(cli.WorktreesResponse{Worktrees: []cli.WorktreesRecord{
		{Path: "/a", Branch: "agent/create", Source: "create", Exists: true},
		{Path: "/b", Branch: "agent/task", Source: "acquire", Exists: false},
	}})
	want := "agent/create                 create   present    /a\nagent/task                   acquire  missing    /b\n"
	if populated != want {
		t.Fatalf("populated human output = %q, want %q", populated, want)
	}
}

func TestApplicationRoutesWorktreesJSONThroughInjectedDependencies(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	path := filepath.Join(filepath.Dir(root), "slot")
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	repository := git.Repository{Root: root, CommonDir: filepath.Join(root, ".git")}
	queries := cli.QueryDependencies{
		ReadWorktreeRegistry: func(_ context.Context, commonDir string) (state.WorktreeRegistry, error) {
			if commonDir != repository.CommonDir {
				t.Fatalf("commonDir = %q, want %q", commonDir, repository.CommonDir)
			}
			return state.WorktreeRegistry{
				Version: state.WorktreeRegistryVersion,
				Worktrees: map[string]state.WorktreeRecord{
					key: {
						Path: path, Branch: "agent/task", Source: state.WorktreeSourceCreate,
						CreatedAt: "2026-08-19T10:00:00.000Z", UpdatedAt: "2026-08-19T10:00:00.000Z",
					},
				},
			}, nil
		},
		WorktreePathExists: func(value string) bool { return value == path },
	}
	var stdout bytes.Buffer
	application := cli.New(cli.Options{
		Version: "0.3.0-test", CWD: root, Stdout: &stdout,
		DiscoverRepository: func(_ context.Context, cwd string) (git.Repository, error) {
			if cwd != root {
				t.Fatalf("discovery cwd = %q, want %q", cwd, root)
			}
			return repository, nil
		},
		Queries: queries,
	})
	code, err := application.Run(context.Background(), []string{"worktrees", "--json"})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if strings.Count(stdout.String(), "\n") != 1 {
		t.Fatalf("stdout = %q, want exactly one JSON value", stdout.String())
	}
	var decoded cli.WorktreesResponse
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout = %q: %v", stdout.String(), err)
	}
	if decoded.Repository != root || len(decoded.Worktrees) != 1 || decoded.Worktrees[0].Path != path || !decoded.Worktrees[0].Exists {
		t.Fatalf("decoded = %#v", decoded)
	}
}

func TestParseWorktreesJSONAndRejectsPositionalArguments(t *testing.T) {
	got, err := cli.Parse([]string{"worktrees", "--json"})
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}
	if got.Name != "worktrees" || !got.JSON {
		t.Fatalf("invocation = %#v", got)
	}
	_, err = cli.Parse([]string{"worktrees", "extra"})
	if err == nil || !strings.Contains(err.Error(), "worktrees does not accept positional arguments") {
		t.Fatalf("positional error = %v", err)
	}
}
