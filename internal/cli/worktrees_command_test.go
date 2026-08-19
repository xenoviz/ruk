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
	"github.com/xenoviz/ruk/internal/worktrees"
)

func TestHandleWorktreesSortsByPathAndReportsExistence(t *testing.T) {
	base := t.TempDir()
	later := filepath.Join(base, "z-workspace")
	earlier := filepath.Join(base, "a-workspace")
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
	got, err = cli.Parse([]string{"worktrees", "--all", "--json"})
	if err != nil {
		t.Fatalf("Parse --all returned an error: %v", err)
	}
	if got.Name != "worktrees" || !got.JSON || !got.All {
		t.Fatalf("invocation = %#v", got)
	}
}

func TestHandleAllWorktreesAggregatesSortedRepositories(t *testing.T) {
	base := t.TempDir()
	laterRoot := filepath.Join(base, "z-repo")
	earlierRoot := filepath.Join(base, "a-repo")
	laterCommon := filepath.Join(laterRoot, ".git")
	earlierCommon := filepath.Join(earlierRoot, ".git")
	missingRoot := filepath.Join(base, "missing")
	emptyRoot := filepath.Join(base, "empty")
	missingCommon := filepath.Join(missingRoot, ".git")
	emptyCommon := filepath.Join(emptyRoot, ".git")
	laterPath := filepath.Join(laterRoot, "z-slot")
	earlierLate := filepath.Join(earlierRoot, "z-slot")
	earlierEarly := filepath.Join(earlierRoot, "a-slot")
	laterKey, err := state.TreeKey(laterCommon)
	if err != nil {
		t.Fatal(err)
	}
	earlierKey, err := state.TreeKey(earlierCommon)
	if err != nil {
		t.Fatal(err)
	}
	missingKey, err := state.TreeKey(missingCommon)
	if err != nil {
		t.Fatal(err)
	}
	emptyKey, err := state.TreeKey(emptyCommon)
	if err != nil {
		t.Fatal(err)
	}
	laterWorktreeKey, err := state.TreeKey(laterPath)
	if err != nil {
		t.Fatal(err)
	}
	earlierLateKey, err := state.TreeKey(earlierLate)
	if err != nil {
		t.Fatal(err)
	}
	earlierEarlyKey, err := state.TreeKey(earlierEarly)
	if err != nil {
		t.Fatal(err)
	}
	queries := cli.QueryDependencies{
		ReadWorktreeIndex: func(context.Context) (worktrees.Index, error) {
			return worktrees.Index{
				Version: worktrees.IndexVersion,
				Repositories: map[string]worktrees.RepositoryRecord{
					laterKey:   {CommonDir: laterCommon, Root: laterRoot, UpdatedAt: "2026-08-19T11:00:00.000Z"},
					earlierKey: {CommonDir: earlierCommon, Root: earlierRoot, UpdatedAt: "2026-08-19T10:00:00.000Z"},
					missingKey: {CommonDir: missingCommon, Root: missingRoot, UpdatedAt: "2026-08-19T09:00:00.000Z"},
					emptyKey:   {CommonDir: emptyCommon, Root: emptyRoot, UpdatedAt: "2026-08-19T08:00:00.000Z"},
				},
			}, nil
		},
		WorktreePathExists: func(path string) bool {
			return path != missingCommon && path != earlierLate
		},
		ReadWorktreeRegistry: func(_ context.Context, commonDir string) (state.WorktreeRegistry, error) {
			switch commonDir {
			case laterCommon:
				return state.WorktreeRegistry{
					Version: state.WorktreeRegistryVersion,
					Worktrees: map[string]state.WorktreeRecord{
						laterWorktreeKey: {Path: laterPath, Branch: "agent/later", Source: state.WorktreeSourceWarm, CreatedAt: "2026-08-19T11:00:00.000Z", UpdatedAt: "2026-08-19T11:00:00.000Z"},
					},
				}, nil
			case earlierCommon:
				return state.WorktreeRegistry{
					Version: state.WorktreeRegistryVersion,
					Worktrees: map[string]state.WorktreeRecord{
						earlierLateKey:  {Path: earlierLate, Branch: "agent/late", Source: state.WorktreeSourceCreate, CreatedAt: "2026-08-19T10:30:00.000Z", UpdatedAt: "2026-08-19T10:30:00.000Z"},
						earlierEarlyKey: {Path: earlierEarly, Branch: "agent/early", Source: state.WorktreeSourceAcquire, CreatedAt: "2026-08-19T10:00:00.000Z", UpdatedAt: "2026-08-19T10:00:00.000Z"},
					},
				}, nil
			case emptyCommon:
				return *state.EmptyWorktreeRegistry(), nil
			default:
				return state.WorktreeRegistry{}, errors.New("unexpected common dir " + commonDir)
			}
		},
	}
	got, err := queries.HandleAllWorktrees(context.Background())
	if err != nil {
		t.Fatalf("HandleAllWorktrees returned an error: %v", err)
	}
	if len(got.Repositories) != 2 || got.Repositories[0].Repository != earlierRoot || got.Repositories[1].Repository != laterRoot {
		t.Fatalf("repositories = %#v", got.Repositories)
	}
	if len(got.Repositories[0].Worktrees) != 2 || got.Repositories[0].Worktrees[0].Path != earlierEarly || got.Repositories[0].Worktrees[1].Path != earlierLate {
		t.Fatalf("earlier records were not sorted by path: %#v", got.Repositories[0].Worktrees)
	}
	if !got.Repositories[0].Worktrees[0].Exists || got.Repositories[0].Worktrees[1].Exists {
		t.Fatalf("exists flags = %#v", got.Repositories[0].Worktrees)
	}
}

func TestHandleAllWorktreesRequiresDependenciesAndWrapsRegistryErrors(t *testing.T) {
	_, err := (cli.QueryDependencies{}).HandleAllWorktrees(context.Background())
	if err == nil || !strings.Contains(err.Error(), "worktrees query dependencies are incomplete") {
		t.Fatalf("empty dependencies error = %v", err)
	}
	root := filepath.Join(t.TempDir(), "broken")
	commonDir := filepath.Join(root, ".git")
	key, err := state.TreeKey(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	queries := cli.QueryDependencies{
		ReadWorktreeIndex: func(context.Context) (worktrees.Index, error) {
			return worktrees.Index{Version: worktrees.IndexVersion, Repositories: map[string]worktrees.RepositoryRecord{
				key: {CommonDir: commonDir, Root: root, UpdatedAt: "2026-08-19T10:00:00.000Z"},
			}}, nil
		},
		WorktreePathExists: func(string) bool { return true },
		ReadWorktreeRegistry: func(context.Context, string) (state.WorktreeRegistry, error) {
			return state.WorktreeRegistry{}, errors.New("registry unreadable")
		},
	}
	_, err = queries.HandleAllWorktrees(context.Background())
	if err == nil || !strings.Contains(err.Error(), "read worktree registry for "+root) || !strings.Contains(err.Error(), "registry unreadable") {
		t.Fatalf("wrapped error = %v", err)
	}
}

func TestFormatAllWorktreesJSONEmitsExactlyOneValueWithEmptyArray(t *testing.T) {
	encoded, err := cli.FormatAllWorktrees(cli.AllWorktreesResponse{Repositories: []cli.WorktreesResponse{}}, true)
	if err != nil {
		t.Fatalf("FormatAllWorktrees returned an error: %v", err)
	}
	if !strings.HasSuffix(encoded, "\n") || strings.Count(encoded, "\n") != 1 {
		t.Fatalf("JSON output = %q, want exactly one JSON value ending in a newline", encoded)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("JSON output is not one value: %v", err)
	}
	repositories, ok := decoded["repositories"].([]any)
	if !ok || repositories == nil {
		t.Fatalf("repositories = %#v, want []", decoded["repositories"])
	}
	if len(repositories) != 0 {
		t.Fatalf("repositories = %#v, want empty array", repositories)
	}
}

func TestFormatAllWorktreesHumanEmptyAndPopulated(t *testing.T) {
	empty := cli.FormatAllWorktreesHuman(cli.AllWorktreesResponse{Repositories: []cli.WorktreesResponse{}})
	if empty != "No Ruk-created worktrees are tracked on this host.\n" {
		t.Fatalf("empty human output = %q", empty)
	}
	populated := cli.FormatAllWorktreesHuman(cli.AllWorktreesResponse{Repositories: []cli.WorktreesResponse{
		{Repository: "/a", Worktrees: []cli.WorktreesRecord{{Path: "/a/one", Branch: "agent/one", Source: "acquire", Exists: true}}},
		{Repository: "/b", Worktrees: []cli.WorktreesRecord{{Path: "/b/two", Branch: "agent/two", Source: "create", Exists: false}}},
	}})
	want := "/a:\n  agent/one                    acquire  present    /a/one\n\n/b:\n  agent/two                    create   missing    /b/two\n"
	if populated != want {
		t.Fatalf("populated human output = %q, want %q", populated, want)
	}
}

func TestApplicationRoutesWorktreesAllWithoutDiscoveringARepository(t *testing.T) {
	queries := cli.QueryDependencies{
		ReadWorktreeIndex: func(context.Context) (worktrees.Index, error) {
			return *worktrees.EmptyIndex(), nil
		},
		ReadWorktreeRegistry: func(context.Context, string) (state.WorktreeRegistry, error) {
			return *state.EmptyWorktreeRegistry(), nil
		},
		WorktreePathExists: func(string) bool { return true },
	}
	var stdout bytes.Buffer
	application := cli.New(cli.Options{
		Version: "0.3.0-test", CWD: "/missing", Stdout: &stdout,
		DiscoverRepository: func(context.Context, string) (git.Repository, error) {
			return git.Repository{}, errors.New("discovery must not run for worktrees --all")
		},
		Queries: queries,
	})
	code, err := application.Run(context.Background(), []string{"worktrees", "--all", "--json"})
	if err != nil || code != 0 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if strings.Count(stdout.String(), "\n") != 1 || !strings.Contains(stdout.String(), `"repositories":[]`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
