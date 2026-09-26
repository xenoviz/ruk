package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/dashboard"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
	"github.com/xenoviz/ruk/internal/worktrees"
)

// uiFixture is one repository with every kind of worktree the dashboard
// distinguishes, plus a second indexed repository whose state is unreadable.
type uiFixture struct {
	root, broken                       string
	repository                         git.Repository
	assigned, available, created, mine string
	now                                time.Time
}

func newUIFixture(t *testing.T) uiFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "app")
	return uiFixture{
		root:       root,
		broken:     filepath.Join(base, "broken"),
		repository: git.Repository{Root: root, CommonDir: filepath.Join(root, ".git"), PrimaryRoot: root, PrimaryCheckout: true},
		assigned:   filepath.Join(base, "app-ruk-agent-a"),
		available:  filepath.Join(base, "app-ruk-warm-0"),
		created:    filepath.Join(base, "app-feature-b"),
		mine:       filepath.Join(base, "app-hand-made"),
		now:        time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC),
	}
}

func (fixture uiFixture) key(t *testing.T, path string) string {
	t.Helper()
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func (fixture uiFixture) options(t *testing.T) Options {
	t.Helper()
	failure := "pnpm install exited with status 1"
	started := "2026-09-26T09:55:00.000Z"
	snapshot := state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{
		fixture.key(t, fixture.assigned): {Path: fixture.assigned, Fingerprint: "f", Mode: "managed-install"},
	}, Workspaces: map[string]state.WorkspaceRecord{
		fixture.key(t, fixture.assigned): {
			Path: fixture.assigned, Managed: true, Branch: "agent/a", Lifecycle: state.LifecycleAssigned,
			Assignment: &state.AssignmentRecord{
				ID: "assignment-1", Owner: "claude-1", AssignedAt: "2026-09-26T09:00:00.000Z", RenewedAt: "2026-09-26T09:00:00.000Z",
				ExpiresAt: "2026-09-26T11:00:00.000Z", LeaseDurationMinutes: 120, LastActivityAt: "2026-09-26T09:58:00.000Z",
				Ports: map[string]int64{"web": 4102},
			},
			Processes: []state.TrackedProcessRecord{{PID: 4321, Command: []string{"bun", "test"}, StartedAt: started}},
		},
		fixture.key(t, fixture.available): {Path: fixture.available, Managed: true, Lifecycle: state.LifecycleFailed, Failure: &failure},
	}}
	return Options{
		Version: "test", CWD: fixture.root, Now: func() time.Time { return fixture.now },
		DiscoverRepository: func(_ context.Context, cwd string) (git.Repository, error) {
			switch {
			case strings.HasPrefix(cwd, fixture.root):
				return fixture.repository, nil
			case cwd == fixture.broken:
				return git.Repository{Root: fixture.broken, CommonDir: filepath.Join(fixture.broken, ".git"), PrimaryRoot: fixture.broken}, nil
			}
			return git.Repository{}, errors.New("not a Git repository")
		},
		Queries: QueryDependencies{
			ReadWorktreeIndex: func(context.Context) (worktrees.Index, error) {
				return worktrees.Index{Repositories: map[string]worktrees.RepositoryRecord{
					"app":    {Root: fixture.root, CommonDir: fixture.repository.CommonDir},
					"broken": {Root: fixture.broken, CommonDir: filepath.Join(fixture.broken, ".git")},
					"gone":   {Root: "/deleted", CommonDir: "/deleted/.git"},
				}}, nil
			},
			WorktreePathExists: func(path string) bool { return path != "/deleted/.git" },
			ReadState: func(_ context.Context, commonDir string) (state.State, error) {
				if commonDir != fixture.repository.CommonDir {
					return state.State{}, errors.New("state file is corrupt")
				}
				return snapshot, nil
			},
			ListWorktrees: func(context.Context, string) ([]git.WorktreeRecord, error) {
				return []git.WorktreeRecord{
					{Path: fixture.root, Branch: "main", Head: "abc"},
					{Path: fixture.assigned, Branch: "agent/a", Head: "abc"},
					{Path: fixture.available, Head: "abc"},
					{Path: fixture.created, Branch: "feature/b", Head: "abc"},
					{Path: fixture.mine, Branch: "mine", Head: "abc"},
				}, nil
			},
			ReadWorktreeRegistry: func(context.Context, string) (state.WorktreeRegistry, error) {
				return state.WorktreeRegistry{Version: state.WorktreeRegistryVersion, Worktrees: map[string]state.WorktreeRecord{
					fixture.key(t, fixture.assigned): {Path: fixture.assigned, Source: state.WorktreeSourceAcquire},
					fixture.key(t, fixture.created):  {Path: fixture.created, Source: state.WorktreeSourceCreate},
				}}, nil
			},
		},
	}
}

func TestParseUIGrammar(t *testing.T) {
	invocation, err := Parse([]string{"ui", "--port", "0", "--port", "47213", "--open", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Name != "ui" || !invocation.Open || !invocation.JSON || strings.Join(invocation.Ports, ",") != "0,47213" {
		t.Fatalf("invocation = %+v", invocation)
	}
	for _, args := range [][]string{{"ui", "extra"}, {"ui", "--force"}, {"ui", "--port"}} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) error = nil", args)
		}
	}
}

func TestParseUIPort(t *testing.T) {
	for _, tc := range []struct {
		values []string
		want   int
		ok     bool
	}{
		{nil, 0, true}, {[]string{"8080"}, 8080, true}, {[]string{"1", "65535"}, 65535, true},
		{[]string{"-1"}, 0, false}, {[]string{"65536"}, 0, false}, {[]string{"http"}, 0, false},
	} {
		got, err := parseUIPort(tc.values)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("parseUIPort(%q) = %d, %v", tc.values, got, err)
		}
	}
}

func TestUISnapshotShowsRukWorkspacesWithStateDetails(t *testing.T) {
	fixture := newUIFixture(t)
	source := newUISource(New(fixture.options(t)))
	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Repositories) != 2 {
		t.Fatalf("repositories = %+v, want app and broken (cwd deduplicated, deleted skipped)", snapshot.Repositories)
	}
	app, broken := snapshot.Repositories[0], snapshot.Repositories[1]
	if app.Root != fixture.root || app.Name != "app" || app.Error != nil {
		t.Fatalf("app repository = %+v", app)
	}
	if broken.Error == nil || !strings.Contains(*broken.Error, "state file is corrupt") || len(broken.Workspaces) != 0 {
		t.Fatalf("an unreadable repository must report its error, got %+v", broken)
	}
	paths := map[string]dashboard.Workspace{}
	for _, workspace := range app.Workspaces {
		paths[workspace.Path] = workspace
	}
	if len(paths) != 3 {
		t.Fatalf("workspaces = %+v, want assigned, failed, and created (no primary, no untracked worktree)", app.Workspaces)
	}
	if _, ok := paths[fixture.mine]; ok {
		t.Fatal("a worktree Ruk neither manages nor created must not appear")
	}
	assigned := paths[fixture.assigned]
	if *assigned.Lifecycle != "assigned" || *assigned.Owner != "claude-1" || *assigned.AssignmentID != "assignment-1" ||
		*assigned.LeaseMinutes != 120 || assigned.Ports["web"] != 4102 || *assigned.Source != "acquire" || !assigned.Prepared {
		t.Fatalf("assigned workspace = %+v", assigned)
	}
	if len(assigned.Processes) != 1 || assigned.Processes[0].PID != 4321 || strings.Join(assigned.Processes[0].Command, " ") != "bun test" {
		t.Fatalf("processes = %+v", assigned.Processes)
	}
	failed := paths[fixture.available]
	if *failed.Lifecycle != "failed" || failed.Failure == nil || *failed.Failure != "pnpm install exited with status 1" || failed.Source != nil {
		t.Fatalf("failed workspace = %+v", failed)
	}
	created := paths[fixture.created]
	if created.Lifecycle != nil || created.Managed || *created.Source != "create" {
		t.Fatalf("created worktree = %+v", created)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || bytes.Contains(encoded, []byte(`"ports":null`)) || bytes.Contains(encoded, []byte(`"processes":null`)) {
		t.Fatalf("snapshot JSON must use empty collections, got %s (%v)", encoded, err)
	}
}

func TestUIActionArgumentsMatchCLICommands(t *testing.T) {
	for _, tc := range []struct {
		action dashboard.Action
		args   string
		json   bool
	}{
		{dashboard.Action{Kind: dashboard.ActionRenew, AssignmentID: "a"}, "renew a --json", true},
		{dashboard.Action{Kind: dashboard.ActionRenew, AssignmentID: "a", TTLMinutes: 90}, "renew a --json --ttl 90", true},
		{dashboard.Action{Kind: dashboard.ActionRelease, AssignmentID: "a"}, "release a --json", true},
		{dashboard.Action{Kind: dashboard.ActionRelease, AssignmentID: "a", Force: true}, "release a --json --force", true},
		{dashboard.Action{Kind: dashboard.ActionRemove, Path: "/w"}, "remove /w", false},
		{dashboard.Action{Kind: dashboard.ActionGCPreview}, "gc --json", true},
		{dashboard.Action{Kind: dashboard.ActionGCApply}, "gc --apply --json", true},
		{dashboard.Action{Kind: dashboard.ActionGCApply, ForceExpired: true}, "gc --apply --json --force-expired", true},
		{dashboard.Action{Kind: dashboard.ActionDisk}, "stats --disk --json", true},
	} {
		args, jsonOutput := uiActionArgs(tc.action)
		if strings.Join(args, " ") != tc.args || jsonOutput != tc.json {
			t.Errorf("%+v -> %q (json %v), want %q (json %v)", tc.action, args, jsonOutput, tc.args, tc.json)
		}
		if _, err := Parse(args); err != nil {
			t.Errorf("%q is not a valid CLI invocation: %v", args, err)
		}
	}
}

func TestUIActRunsInKnownRepositoryOnly(t *testing.T) {
	fixture := newUIFixture(t)
	options := fixture.options(t)
	options.CWD = fixture.assigned // ruk ui started inside a workspace of the target repository
	source := newUISource(New(options))
	var gotCWD string
	var gotArgs []string
	source.run = func(_ context.Context, cwd string, args []string) (string, error) {
		gotCWD, gotArgs = cwd, args
		return `{"status":"planned","removed":[],"expired":[]}` + "\n", nil
	}
	result, err := source.Act(context.Background(), dashboard.Action{Kind: dashboard.ActionGCPreview, Repository: fixture.root})
	if err != nil {
		t.Fatal(err)
	}
	if gotCWD != fixture.assigned || strings.Join(gotArgs, " ") != "gc --json" {
		t.Fatalf("ran %q in %q; commands must run from the invoking workspace so it stays protected", gotArgs, gotCWD)
	}
	if string(result.Result) != `{"status":"planned","removed":[],"expired":[]}` {
		t.Fatalf("result = %s", result.Result)
	}

	_, err = source.Act(context.Background(), dashboard.Action{Kind: dashboard.ActionGCApply, Repository: "/somewhere/else"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != string(InvalidArgumentCode) {
		t.Fatalf("unknown repository error = %v", err)
	}
	if _, err := source.Act(context.Background(), dashboard.Action{Kind: "shell", Repository: fixture.root}); err == nil {
		t.Fatal("invalid action reached the command runner")
	}
}

func TestUIActReportsCommandFailuresAsErrorRecords(t *testing.T) {
	fixture := newUIFixture(t)
	source := newUISource(New(fixture.options(t)))
	source.run = func(context.Context, string, []string) (string, error) {
		return "", errors.New("Workspace has uncommitted changes. Commit them or retry release with --force.")
	}
	_, err := source.Act(context.Background(), dashboard.Action{Kind: dashboard.ActionRelease, Repository: fixture.root, AssignmentID: "a"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || actionErr.Code != string(WorkspaceDirtyCode) {
		t.Fatalf("error = %#v, want the CLI's WORKSPACE_DIRTY record", err)
	}

	source.run = func(context.Context, string, []string) (string, error) { return "not json", nil }
	if _, err := source.Act(context.Background(), dashboard.Action{Kind: dashboard.ActionGCPreview, Repository: fixture.root}); err == nil {
		t.Fatal("non-JSON output from a JSON command was accepted")
	}
	source.run = func(context.Context, string, []string) (string, error) { return "Removed /w\n", nil }
	result, err := source.Act(context.Background(), dashboard.Action{Kind: dashboard.ActionRemove, Repository: fixture.root, Path: "/w"})
	if err != nil || result.Message != "Removed /w" || result.Result != nil {
		t.Fatalf("remove result = %+v, %v", result, err)
	}
}

func TestUIActRunsTheRealCommandInProcess(t *testing.T) {
	fixture := newUIFixture(t)
	options := fixture.options(t)
	var renewed []string
	options.Renew = func(_ context.Context, repository git.Repository, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
		if repository != fixture.repository {
			t.Errorf("renew repository = %+v", repository)
		}
		renewed = append(renewed, assignmentID+"@"+expiresAt.Sub(fixture.now).String())
		if assignmentID == "stale" {
			return state.WorkspaceRecord{}, errors.New("assignment stale is no longer assigned")
		}
		return state.WorkspaceRecord{Path: fixture.assigned, Assignment: &state.AssignmentRecord{ID: assignmentID, ExpiresAt: "2026-09-26T11:30:00.000Z"}}, nil
	}
	source := newUISource(New(options))
	result, err := source.Act(context.Background(), dashboard.Action{Kind: dashboard.ActionRenew, Repository: fixture.root, AssignmentID: "assignment-1", TTLMinutes: 90})
	if err != nil {
		t.Fatal(err)
	}
	var record RenewRecord
	if err := json.Unmarshal(result.Result, &record); err != nil || record.Status != "renewed" || record.ExpiresAt != "2026-09-26T11:30:00.000Z" {
		t.Fatalf("renew result = %s (%v)", result.Result, err)
	}
	_, err = source.Act(context.Background(), dashboard.Action{Kind: dashboard.ActionRenew, Repository: fixture.root, AssignmentID: "stale"})
	var actionErr *dashboard.ActionError
	if !errors.As(err, &actionErr) || !strings.Contains(actionErr.Message, "no longer assigned") {
		t.Fatalf("stale renew error = %v", err)
	}
	if strings.Join(renewed, ",") != "assignment-1@1h30m0s,stale@8h0m0s" {
		t.Fatalf("renew calls = %q", renewed)
	}
}

// lockedBuffer lets the test read stdout while runUI is still writing.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestRunUIServesUntilCancelledAndPrintsOneJSONValue(t *testing.T) {
	fixture := newUIFixture(t)
	options := fixture.options(t)
	var stdout, stderr lockedBuffer
	options.Stdout, options.Stderr = &stdout, &stderr
	listened := make(chan string, 1)
	options.UIListen = func(network, address string) (net.Listener, error) {
		listened <- network + " " + address
		return net.Listen(network, "127.0.0.1:0")
	}
	opened := make(chan string, 1)
	options.UIOpen = func(url string) error {
		opened <- url
		return errors.New("no display")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var code int
	go func() {
		var err error
		code, err = New(options).Run(ctx, []string{"ui", "--port", "0", "--open", "--json"})
		done <- err
	}()
	if got := <-listened; got != "tcp 127.0.0.1:0" {
		t.Fatalf("listen = %q, want loopback only", got)
	}
	url := <-opened
	var listening uiListening
	if err := json.Unmarshal([]byte(stdout.String()), &listening); err != nil {
		t.Fatalf("stdout %q is not one JSON value: %v", stdout.String(), err)
	}
	if listening.Status != "listening" || listening.URL != url || !strings.HasPrefix(url, "http://"+listening.Address+"/?token=") {
		t.Fatalf("listening = %+v, opened %q", listening, url)
	}
	if !strings.Contains(stderr.String(), "could not open a browser: no display") {
		t.Fatalf("stderr = %q, want a warning that does not stop the server", stderr.String())
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	response, err = client.Get("http://" + listening.Address + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot dashboard.Snapshot
	err = json.NewDecoder(response.Body).Decode(&snapshot)
	_ = response.Body.Close()
	if err != nil || len(snapshot.Repositories) != 2 || snapshot.Version != "test" {
		t.Fatalf("snapshot = %+v (%v)", snapshot, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil || code != 0 {
			t.Fatalf("runUI = %d, %v", code, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runUI did not stop after cancellation")
	}
}

func TestRunUIFailures(t *testing.T) {
	fixture := newUIFixture(t)
	options := fixture.options(t)
	options.UIListen = func(string, string) (net.Listener, error) { return nil, errors.New("address already in use") }
	code, err := New(options).Run(context.Background(), []string{"ui", "--port", "47213"})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "port 47213: address already in use") {
		t.Fatalf("listen failure = %d, %v", code, err)
	}
	code, err = New(options).Run(context.Background(), []string{"ui", "--port", "99999"})
	if code != 1 || err == nil || ClassifyError(err).Code != InvalidArgumentCode {
		t.Fatalf("invalid port = %d, %v (%s)", code, err, ClassifyError(err).Code)
	}
}

func TestRunUIPrintsHumanAddress(t *testing.T) {
	fixture := newUIFixture(t)
	options := fixture.options(t)
	var stdout bytes.Buffer
	options.Stdout = &stdout
	options.UIListen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, err := New(options).runUI(ctx, Invocation{Name: "ui"})
	if err != nil || code != 0 {
		t.Fatalf("runUI = %d, %v", code, err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "Ruk dashboard: http://127.0.0.1:") || !strings.Contains(lines[0], "/?token=") || lines[1] != "Press Ctrl+C to stop." {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
