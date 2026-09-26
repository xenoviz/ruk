package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/dashboard"
	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

// uiSource backs the dashboard with the same readers and commands as the CLI.
//
// Repositories come from the host index plus the checkout ruk ui started in.
// The index is display-only discovery: every action re-discovers the
// repository through Git and then runs the ordinary command inside it, so
// assignment fences, locks, and error records are exactly the CLI's.
type uiSource struct {
	application *Application
	hostname    string
	// run executes one CLI invocation in cwd. Tests replace it to observe
	// the argument mapping.
	run func(ctx context.Context, cwd string, args []string) (string, error)
}

func newUISource(application *Application) *uiSource {
	hostname, _ := os.Hostname()
	source := &uiSource{application: application, hostname: hostname}
	source.run = source.runCommand
	return source
}

type uiRepository struct {
	root      string
	commonDir string
}

// repositories lists known repositories, deduplicated by common Git
// directory. Missing index entries are skipped, as ruk worktrees --all does.
func (source *uiSource) repositories(ctx context.Context) ([]uiRepository, error) {
	application := source.application
	seen := map[string]bool{}
	result := make([]uiRepository, 0)
	add := func(root, commonDir string) {
		key := filepath.Clean(commonDir)
		if seen[key] {
			return
		}
		seen[key] = true
		result = append(result, uiRepository{root: root, commonDir: commonDir})
	}
	if current, err := application.discover(ctx, application.cwd); err == nil {
		add(primaryRoot(current), current.CommonDir)
	}
	queries := application.queries
	if queries.ReadWorktreeIndex != nil {
		index, err := queries.ReadWorktreeIndex(ctx)
		if err != nil {
			return nil, err
		}
		for _, record := range index.Repositories {
			if queries.WorktreePathExists != nil && !queries.WorktreePathExists(record.CommonDir) {
				continue
			}
			add(record.Root, record.CommonDir)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].root < result[j].root })
	return result, nil
}

func primaryRoot(repository git.Repository) string {
	if repository.PrimaryRoot != "" {
		return repository.PrimaryRoot
	}
	return repository.Root
}

// Snapshot implements dashboard.Source.
func (source *uiSource) Snapshot(ctx context.Context) (dashboard.Snapshot, error) {
	known, err := source.repositories(ctx)
	if err != nil {
		return dashboard.Snapshot{}, err
	}
	now := source.application.now()
	snapshot := dashboard.Snapshot{
		GeneratedAt:  now.UTC().Format(time.RFC3339Nano),
		Host:         source.hostname,
		Version:      source.application.version,
		Repositories: make([]dashboard.Repository, 0, len(known)),
	}
	for _, repository := range known {
		snapshot.Repositories = append(snapshot.Repositories, source.repository(ctx, repository, now))
	}
	return snapshot, nil
}

func (source *uiSource) repository(ctx context.Context, known uiRepository, now time.Time) dashboard.Repository {
	result := dashboard.Repository{Name: filepath.Base(known.root), Root: known.root, CommonDir: known.commonDir, Workspaces: []dashboard.Workspace{}}
	workspaces, err := source.workspaces(ctx, known, now)
	if err != nil {
		message := ClassifyError(err).Message
		result.Error = &message
		return result
	}
	result.Workspaces = workspaces
	return result
}

func (source *uiSource) workspaces(ctx context.Context, known uiRepository, now time.Time) ([]dashboard.Workspace, error) {
	application := source.application
	queries := application.queries
	if queries.ReadState == nil || queries.ListWorktrees == nil {
		return nil, errors.New("dashboard query dependencies are incomplete")
	}
	repository, err := application.discover(ctx, known.root)
	if err != nil {
		return nil, err
	}
	snapshot, err := queries.ReadState(ctx, repository.CommonDir)
	if err != nil {
		return nil, err
	}
	worktrees, err := queries.ListWorktrees(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	registry := state.EmptyWorktreeRegistry()
	if queries.ReadWorktreeRegistry != nil {
		read, err := queries.ReadWorktreeRegistry(ctx, repository.CommonDir)
		if err != nil {
			return nil, err
		}
		registry = &read
	}
	records, err := BuildListResponse(ListQueryInput{Repository: repository, Snapshot: snapshot, Worktrees: worktrees, ObservedAt: now})
	if err != nil {
		return nil, err
	}
	result := make([]dashboard.Workspace, 0, len(records))
	for _, record := range records {
		key, err := state.TreeKey(record.Path)
		if err != nil {
			return nil, err
		}
		tracked, isTracked := registry.Worktrees[key]
		if record.PrimaryCheckout || (!record.Managed && !isTracked) {
			continue
		}
		workspace := dashboard.Workspace{
			Path: record.Path, Branch: record.Branch, Head: record.Head, Managed: record.Managed,
			Prepared: record.Status == "prepared", Mode: record.Mode,
			AssignmentID: record.AssignmentID, ExpiresAt: record.ExpiresAt, LastActivityAt: record.LastActivityAt,
			AutoRenewing: record.AutoRenewing, Ports: map[string]int64{}, Processes: []dashboard.Process{},
		}
		if record.Lifecycle != nil {
			lifecycle := string(*record.Lifecycle)
			workspace.Lifecycle = &lifecycle
		}
		if isTracked {
			sourceName := tracked.Source
			workspace.Source = &sourceName
		}
		if stored, ok := snapshot.Workspaces[key]; ok {
			enrichWorkspace(&workspace, stored)
		}
		result = append(result, workspace)
	}
	return result, nil
}

// enrichWorkspace adds the fields the page shows beyond ruk list: owner,
// lease, ports, tracked processes, and the recorded failure.
func enrichWorkspace(workspace *dashboard.Workspace, stored state.WorkspaceRecord) {
	workspace.Failure = stored.Failure
	for _, process := range stored.Processes {
		command := append([]string{}, process.Command...)
		workspace.Processes = append(workspace.Processes, dashboard.Process{PID: process.PID, Command: command, StartedAt: process.StartedAt})
	}
	assignment := stored.Assignment
	if assignment == nil {
		return
	}
	owner, assignedAt, lease := assignment.Owner, assignment.AssignedAt, assignment.LeaseDurationMinutes
	workspace.Owner = &owner
	workspace.AssignedAt = &assignedAt
	workspace.LeaseMinutes = &lease
	for name, port := range assignment.Ports {
		workspace.Ports[name] = port
	}
}

// Act implements dashboard.Source by running the matching CLI command.
func (source *uiSource) Act(ctx context.Context, action dashboard.Action) (dashboard.ActionResult, error) {
	if err := dashboard.ValidateAction(action); err != nil {
		return dashboard.ActionResult{}, err
	}
	known, err := source.repositories(ctx)
	if err != nil {
		return dashboard.ActionResult{}, err
	}
	var target *uiRepository
	for index := range known {
		if sameQueryPath(known[index].root, action.Repository) {
			target = &known[index]
			break
		}
	}
	if target == nil {
		return dashboard.ActionResult{}, &dashboard.ActionError{Code: string(InvalidArgumentCode), Message: "Unknown repository " + action.Repository}
	}
	repository, err := source.application.discover(ctx, target.root)
	if err != nil {
		return dashboard.ActionResult{}, actionError(err)
	}
	args, jsonOutput := uiActionArgs(action)
	output, err := source.run(ctx, source.commandDirectory(ctx, repository), args)
	if err != nil {
		return dashboard.ActionResult{}, actionError(err)
	}
	output = strings.TrimSpace(output)
	if !jsonOutput {
		return dashboard.ActionResult{Message: output}, nil
	}
	if !json.Valid([]byte(output)) {
		return dashboard.ActionResult{}, fmt.Errorf("%s returned output that is not JSON", args[0])
	}
	return dashboard.ActionResult{Result: json.RawMessage(output)}, nil
}

// uiActionArgs maps a validated action onto CLI arguments. Only remove lacks
// a JSON mode.
func uiActionArgs(action dashboard.Action) ([]string, bool) {
	switch action.Kind {
	case dashboard.ActionRenew:
		args := []string{"renew", action.AssignmentID, "--json"}
		if action.TTLMinutes > 0 {
			args = append(args, "--ttl", strconv.Itoa(action.TTLMinutes))
		}
		return args, true
	case dashboard.ActionRelease:
		args := []string{"release", action.AssignmentID, "--json"}
		if action.Force {
			args = append(args, "--force")
		}
		return args, true
	case dashboard.ActionRemove:
		return []string{"remove", action.Path}, false
	case dashboard.ActionGCPreview:
		return []string{"gc", "--json"}, true
	case dashboard.ActionGCApply:
		args := []string{"gc", "--apply", "--json"}
		if action.ForceExpired {
			args = append(args, "--force-expired")
		}
		return args, true
	default: // ActionDisk; ValidateAction rejects anything else.
		return []string{"stats", "--disk", "--json"}, true
	}
}

// commandDirectory runs commands from the directory ruk ui started in when
// that is inside the target repository, so protections for the invoking
// workspace (remove and gc refuse to delete it) still apply. Otherwise the
// command runs from the repository's primary checkout.
func (source *uiSource) commandDirectory(ctx context.Context, repository git.Repository) string {
	application := source.application
	if current, err := application.discover(ctx, application.cwd); err == nil && sameQueryPath(current.CommonDir, repository.CommonDir) {
		return application.cwd
	}
	return primaryRoot(repository)
}

// runCommand executes one invocation on a copy of the application whose
// working directory and output streams are private to this call.
func (source *uiSource) runCommand(ctx context.Context, cwd string, args []string) (string, error) {
	command := *source.application
	var stdout, stderr bytes.Buffer
	command.cwd = cwd
	command.stdout = &stdout
	command.stderr = &stderr
	command.stdin = strings.NewReader("")
	code, err := command.Run(ctx, args)
	if err != nil {
		return "", err
	}
	if code != 0 {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = fmt.Sprintf("%s exited with status %d", args[0], code)
		}
		return "", errors.New(strings.TrimPrefix(message, "ruk: "))
	}
	return stdout.String(), nil
}

func actionError(err error) error {
	record := ClassifyError(err)
	return &dashboard.ActionError{Code: string(record.Code), Message: record.Message, Retryable: record.Retryable, Recovery: record.Recovery}
}
