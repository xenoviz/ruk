// Package git contains the read-only Git discovery and ref operations used by
// the workspace lifecycle. Worktree mutation deliberately lives elsewhere.
package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CommandResult is the observable result of one Git command.
//
// A runner should return a non-zero ExitCode for an expected Git rejection
// (for example, show-ref not finding a branch) and reserve error for an
// inability to start or communicate with Git.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// CommandRunner is the seam around the Git subprocess. Args contains the
// arguments after the git executable name.
type CommandRunner func(context.Context, string, []string) (CommandResult, error)

// Repository identifies the current checkout, the shared Git directory, and
// the primary checkout that owns it. PrimaryCheckout is true when Root is the
// primary checkout, including repositories configured with a separate Git
// directory.
type Repository struct {
	Root            string
	CommonDir       string
	PrimaryRoot     string
	PrimaryCheckout bool
}

// Client groups Git operations around an injected runner.
type Client struct {
	Runner CommandRunner
}

// NewClient returns a Git client. A nil runner uses the operating system Git
// executable.
func NewClient(runner CommandRunner) Client {
	if runner == nil {
		runner = OSCommandRunner
	}
	return Client{Runner: runner}
}

// OSCommandRunner executes the Git executable and captures its output.
func OSCommandRunner(ctx context.Context, cwd string, args []string) (CommandResult, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = cwd
	stdout, err := command.Output()
	result := CommandResult{Stdout: string(stdout)}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.Stderr = string(exitError.Stderr)
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return CommandResult{}, err
}

// Discover finds the current repository and normalizes every returned path to
// an absolute, cleaned path. Git emits a relative common directory for some
// configurations, so it is resolved relative to cwd rather than the process
// working directory.
func Discover(ctx context.Context, cwd string, runner CommandRunner) (Repository, error) {
	client := NewClient(runner)
	return client.Discover(ctx, cwd)
}

// DiscoverRepository is an explicit alias for Discover.
func DiscoverRepository(ctx context.Context, cwd string, runner CommandRunner) (Repository, error) {
	return Discover(ctx, cwd, runner)
}

// Discover finds the current repository using the client's runner.
func (client Client) Discover(ctx context.Context, cwd string) (Repository, error) {
	if client.Runner == nil {
		client.Runner = OSCommandRunner
	}
	base, err := absoluteClean(cwd)
	if err != nil {
		return Repository{}, err
	}
	rootResult, err := client.run(ctx, base, []string{"rev-parse", "--show-toplevel"})
	if err != nil {
		return Repository{}, fmt.Errorf("discover repository root: %w", err)
	}
	commonResult, err := client.run(ctx, base, []string{"rev-parse", "--path-format=absolute", "--git-common-dir"})
	if err != nil {
		return Repository{}, fmt.Errorf("discover Git common directory: %w", err)
	}
	gitDirResult, err := client.run(ctx, base, []string{"rev-parse", "--path-format=absolute", "--git-dir"})
	if err != nil {
		return Repository{}, fmt.Errorf("discover Git directory: %w", err)
	}
	worktreeResult, err := client.run(ctx, base, []string{"worktree", "list", "--porcelain"})
	if err != nil {
		return Repository{}, fmt.Errorf("discover Git worktrees: %w", err)
	}
	root, err := resolveOutputPath(base, rootResult.Stdout)
	if err != nil {
		return Repository{}, fmt.Errorf("normalize repository root: %w", err)
	}
	commonDir, err := resolveOutputPath(base, commonResult.Stdout)
	if err != nil {
		return Repository{}, fmt.Errorf("normalize Git common directory: %w", err)
	}
	gitDir, err := resolveOutputPath(base, gitDirResult.Stdout)
	if err != nil {
		return Repository{}, fmt.Errorf("normalize Git directory: %w", err)
	}
	worktrees, err := parseWorktreePaths(base, worktreeResult.Stdout)
	if err != nil {
		return Repository{}, fmt.Errorf("normalize Git worktrees: %w", err)
	}
	primaryRoot := root
	primaryCheckout := samePath(commonDir, gitDir)
	if len(worktrees) > 0 {
		primaryRoot = worktrees[0]
		primaryCheckout = samePath(primaryRoot, root)
	}
	return Repository{
		Root:            root,
		CommonDir:       commonDir,
		PrimaryRoot:     primaryRoot,
		PrimaryCheckout: primaryCheckout,
	}, nil
}

// LocalBranchExists reports whether branch resolves to a local branch ref.
func LocalBranchExists(ctx context.Context, cwd, branch string, runner CommandRunner) (bool, error) {
	return NewClient(runner).LocalBranchExists(ctx, cwd, branch)
}

// LocalBranchExists reports whether branch resolves to a local branch ref.
func (client Client) LocalBranchExists(ctx context.Context, cwd, branch string) (bool, error) {
	if branch == "" {
		return false, errors.New("branch must not be empty")
	}
	result, err := client.runAllowFailure(ctx, cwd, []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// RefExists reports whether ref is an exact Git ref.
func RefExists(ctx context.Context, cwd, ref string, runner CommandRunner) (bool, error) {
	return NewClient(runner).RefExists(ctx, cwd, ref)
}

// RefExists reports whether ref is an exact Git ref.
func (client Client) RefExists(ctx context.Context, cwd, ref string) (bool, error) {
	if ref == "" {
		return false, errors.New("ref must not be empty")
	}
	result, err := client.runAllowFailure(ctx, cwd, []string{"show-ref", "--verify", "--quiet", ref})
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// ListRemotes returns configured remote names in Git's order.
func ListRemotes(ctx context.Context, cwd string, runner CommandRunner) ([]string, error) {
	return NewClient(runner).ListRemotes(ctx, cwd)
}

// ListRemotes returns configured remote names in Git's order.
func (client Client) ListRemotes(ctx context.Context, cwd string) ([]string, error) {
	result, err := client.run(ctx, cwd, []string{"remote"})
	if err != nil {
		return nil, fmt.Errorf("list Git remotes: %w", err)
	}
	var remotes []string
	for _, line := range strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			remotes = append(remotes, name)
		}
	}
	return remotes, nil
}

// CurrentBranch returns the current branch, or an empty string when detached.
func CurrentBranch(ctx context.Context, cwd string, runner CommandRunner) (string, error) {
	return NewClient(runner).CurrentBranch(ctx, cwd)
}

// CurrentBranch returns the current branch, or an empty string when detached.
func (client Client) CurrentBranch(ctx context.Context, cwd string) (string, error) {
	result, err := client.run(ctx, cwd, []string{"branch", "--show-current"})
	if err != nil {
		return "", fmt.Errorf("read current branch: %w", err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

// SelectRemote chooses the remote relevant to startPoint. A qualified
// refs/remotes/<remote>/<branch> or shorthand <remote>/<branch> start point is
// validated against configured remotes. A shorthand that is also a local
// branch is treated as a local start point, never as a remote. Without an
// explicit remote start point, origin wins, then a sole remote, and finally
// ambiguity is an error.
func SelectRemote(ctx context.Context, cwd, startPoint string, runner CommandRunner) (string, error) {
	return NewClient(runner).SelectRemote(ctx, cwd, startPoint)
}

// SelectRemote chooses a configured remote using the client's runner.
func (client Client) SelectRemote(ctx context.Context, cwd, startPoint string) (string, error) {
	remotes, err := client.ListRemotes(ctx, cwd)
	if err != nil {
		return "", err
	}
	if name, explicit, err := client.remoteFromStartPoint(ctx, cwd, startPoint); err != nil {
		return "", err
	} else if explicit {
		if !contains(remotes, name) {
			return "", fmt.Errorf("Git remote %s does not exist", name)
		}
		return name, nil
	}

	if contains(remotes, "origin") {
		return "origin", nil
	}

	switch len(remotes) {
	case 0:
		return "", errors.New("Git remote does not exist")
	case 1:
		return remotes[0], nil
	default:
		return "", errors.New("Multiple Git remotes exist; use --from to select one explicitly")
	}
}

// FetchCommand builds the Git arguments for fetching from a remote. The
// optional startPoint is accepted for callers that carry it through the
// acquisition flow, but fetching updates the remote's refs as a whole.
func FetchCommand(remote string, _ ...string) ([]string, error) {
	if remote == "" {
		return nil, errors.New("remote must not be empty")
	}
	return []string{"fetch", "--prune", remote}, nil
}

// Fetch executes the command built by FetchCommand.
func Fetch(ctx context.Context, cwd, remote, ref string, runner CommandRunner) error {
	return NewClient(runner).Fetch(ctx, cwd, remote, ref)
}

// Fetch executes the command built by FetchCommand.
func (client Client) Fetch(ctx context.Context, cwd, remote, ref string) error {
	args, err := FetchCommand(remote, ref)
	if err != nil {
		return err
	}
	if _, err := client.run(ctx, cwd, args); err != nil {
		return fmt.Errorf("fetch %s %s: %w", remote, ref, err)
	}
	return nil
}

// FetchDefaultBranch resolves the selected remote's symbolic HEAD, fetches the
// remote, and returns the exact remote-tracking ref. It never guesses a branch
// name from local configuration.
func (client Client) FetchDefaultBranch(ctx context.Context, cwd string) (string, error) {
	remote, err := client.SelectRemote(ctx, cwd, "")
	if err != nil {
		return "", err
	}
	result, err := client.run(ctx, cwd, []string{"ls-remote", "--symref", remote, "HEAD"})
	if err != nil {
		return "", fmt.Errorf("resolve default branch for %s: %w", remote, err)
	}
	branch := ""
	for _, line := range strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && strings.HasPrefix(fields[1], "refs/heads/") && fields[2] == "HEAD" {
			branch = strings.TrimPrefix(fields[1], "refs/heads/")
			break
		}
	}
	if branch == "" {
		return "", fmt.Errorf("Git remote %s does not advertise a default branch", remote)
	}
	ref := "refs/remotes/" + remote + "/" + branch
	if err := client.Fetch(ctx, cwd, remote, ref); err != nil {
		return "", err
	}
	return ref, nil
}

// AddWorktree creates a linked worktree at destination. Existing local
// branches are checked out directly; missing branches are created from
// startPoint. Detached pool workspaces never create or reuse branch names.
func (client Client) AddWorktree(ctx context.Context, cwd, destination, branch, startPoint string, detach bool) error {
	if destination == "" {
		return errors.New("worktree destination must not be empty")
	}
	if startPoint == "" {
		startPoint = "HEAD"
	}

	args := []string{"worktree", "add"}
	if detach {
		args = append(args, "--detach", destination, startPoint)
	} else {
		if branch == "" {
			return errors.New("worktree branch must not be empty")
		}
		exists, err := client.LocalBranchExists(ctx, cwd, branch)
		if err != nil {
			return fmt.Errorf("inspect worktree branch %s: %w", branch, err)
		}
		if exists {
			args = append(args, destination, branch)
		} else {
			args = append(args, "-b", branch, destination, startPoint)
		}
	}

	if _, err := client.run(ctx, cwd, args); err != nil {
		return fmt.Errorf("add worktree %s: %w", destination, err)
	}
	return nil
}

// ReturnWorktree resets a managed checkout to detached, reusable capacity.
// Without force, any tracked or untracked change blocks the operation. The
// listed dependency projections are retained during Git's ignored-file clean.
func (client Client) ReturnWorktree(ctx context.Context, cwd string, force bool, preservedProjections []string) error {
	if !force {
		result, err := client.run(ctx, cwd, []string{"status", "--porcelain"})
		if err != nil {
			return fmt.Errorf("inspect worktree status: %w", err)
		}
		if result.Stdout != "" {
			return errors.New("Workspace has uncommitted changes. Commit them or retry release with --force.")
		}
	} else if _, err := client.run(ctx, cwd, []string{"reset", "--hard", "HEAD"}); err != nil {
		return fmt.Errorf("reset worktree: %w", err)
	}

	cleanArgs := []string{"clean", "-ffdx"}
	for _, projection := range preservedProjections {
		normalized := strings.Trim(strings.ReplaceAll(projection, `\`, "/"), "/")
		cleanArgs = append(cleanArgs, "-e", "/"+escapeCleanPattern(normalized)+"/")
	}
	if _, err := client.run(ctx, cwd, cleanArgs); err != nil {
		return fmt.Errorf("clean worktree: %w", err)
	}
	if _, err := client.run(ctx, cwd, []string{"switch", "--detach"}); err != nil {
		return fmt.Errorf("detach worktree: %w", err)
	}
	return nil
}

// AssignWorktree attaches a pooled detached checkout to branch, creating that
// branch from startPoint only when it does not already exist locally.
func (client Client) AssignWorktree(ctx context.Context, repository, workspace, branch, startPoint string) error {
	if branch == "" {
		return errors.New("worktree branch must not be empty")
	}
	if startPoint == "" {
		startPoint = "HEAD"
	}
	exists, err := client.LocalBranchExists(ctx, repository, branch)
	if err != nil {
		return fmt.Errorf("inspect worktree branch %s: %w", branch, err)
	}
	args := []string{"switch", branch}
	if !exists {
		args = []string{"switch", "-c", branch, startPoint}
	}
	if _, err := client.run(ctx, workspace, args); err != nil {
		return fmt.Errorf("assign worktree %s: %w", workspace, err)
	}
	return nil
}

// LockWorktree protects pooled capacity from ordinary Git maintenance.
func (client Client) LockWorktree(ctx context.Context, cwd, destination string) error {
	if destination == "" {
		return errors.New("worktree destination must not be empty")
	}
	if _, err := client.run(ctx, cwd, []string{"worktree", "lock", "--reason", "ruk pool", destination}); err != nil {
		return fmt.Errorf("lock worktree %s: %w", destination, err)
	}
	return nil
}

// UnlockWorktree permits cleanup of pooled capacity. Git's already-unlocked
// result is idempotent; all other failures remain visible.
func (client Client) UnlockWorktree(ctx context.Context, cwd, destination string) error {
	if destination == "" {
		return errors.New("worktree destination must not be empty")
	}
	result, err := client.runAllowFailure(ctx, cwd, []string{"worktree", "unlock", destination})
	if err != nil {
		return fmt.Errorf("unlock worktree %s: %w", destination, err)
	}
	if result.ExitCode == 0 {
		return nil
	}
	detail := strings.TrimSpace(result.Stderr + "\n" + result.Stdout)
	if strings.Contains(strings.ToLower(detail), "not locked") {
		return nil
	}
	if detail == "" {
		detail = fmt.Sprintf("exit code %d", result.ExitCode)
	}
	return fmt.Errorf("could not unlock worktree %s: %s", destination, detail)
}

// RemoveWorktree removes a linked checkout from the repository.
func (client Client) RemoveWorktree(ctx context.Context, cwd, destination string, force bool) error {
	if destination == "" {
		return errors.New("worktree destination must not be empty")
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, destination)
	if _, err := client.run(ctx, cwd, args); err != nil {
		return fmt.Errorf("remove worktree %s: %w", destination, err)
	}
	return nil
}

func escapeCleanPattern(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		if strings.ContainsRune(`\*?[]`, character) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(character)
	}
	return escaped.String()
}

func (client Client) remoteFromStartPoint(ctx context.Context, cwd, startPoint string) (string, bool, error) {
	if startPoint == "" || strings.HasPrefix(startPoint, "-") {
		return "", false, nil
	}
	if strings.HasPrefix(startPoint, "refs/remotes/") {
		name, err := remoteName(startPoint)
		return name, true, err
	}
	separator := strings.IndexByte(startPoint, '/')
	if separator <= 0 {
		return "", false, nil
	}
	local, err := client.LocalBranchExists(ctx, cwd, startPoint)
	if err != nil {
		return "", false, err
	}
	if local {
		return "", false, nil
	}
	return startPoint[:separator], true, nil
}

func (client Client) run(ctx context.Context, cwd string, args []string) (CommandResult, error) {
	if client.Runner == nil {
		client.Runner = OSCommandRunner
	}
	result, err := client.Runner(ctx, cwd, args)
	if err != nil {
		return CommandResult{}, err
	}
	if result.ExitCode != 0 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return CommandResult{}, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return result, nil
}

func (client Client) runAllowFailure(ctx context.Context, cwd string, args []string) (CommandResult, error) {
	if client.Runner == nil {
		client.Runner = OSCommandRunner
	}
	return client.Runner(ctx, cwd, args)
}

func remoteName(value string) (string, error) {
	if strings.HasPrefix(value, "refs/remotes/") {
		rest := strings.TrimPrefix(value, "refs/remotes/")
		separator := strings.IndexByte(rest, '/')
		if separator <= 0 {
			return "", fmt.Errorf("invalid remote-tracking ref %q", value)
		}
		return rest[:separator], nil
	}
	if strings.ContainsAny(value, "\r\n") || value == "." {
		return "", fmt.Errorf("invalid remote %q", value)
	}
	return value, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func absoluteClean(value string) (string, error) {
	if value == "" {
		value = "."
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func resolveOutputPath(base, output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", errors.New("Git returned an empty path")
	}
	if !filepath.IsAbs(trimmed) {
		trimmed = filepath.Join(base, trimmed)
	}
	return absoluteClean(trimmed)
}

func parseWorktreePaths(base, output string) ([]string, error) {
	var paths []string
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path, err := resolveOutputPath(base, strings.TrimPrefix(line, "worktree "))
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
