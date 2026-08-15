package git

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// WorktreeRecord describes one checkout reported by Git. Branch is
// "(detached)" when Git reports a detached checkout (or does not provide a
// branch field), matching the TypeScript Git boundary.
type WorktreeRecord struct {
	Path   string
	Branch string
	Head   string
}

// ListWorktrees returns the checkouts known to Git, including detached and
// locked checkouts. Lock and prune metadata is deliberately ignored: callers
// need the stable path, branch, and HEAD inventory while Git remains the
// authority for the metadata itself.
func ListWorktrees(ctx context.Context, cwd string, runner CommandRunner) ([]WorktreeRecord, error) {
	return NewClient(runner).ListWorktrees(ctx, cwd)
}

// ListWorktrees returns the checkouts known to Git, including detached and
// locked checkouts.
func (client Client) ListWorktrees(ctx context.Context, cwd string) ([]WorktreeRecord, error) {
	result, err := client.run(ctx, cwd, []string{"worktree", "list", "--porcelain", "-z"})
	if err != nil {
		return nil, fmt.Errorf("list Git worktrees: %w", err)
	}
	records, err := parseWorktreeRecords(cwd, result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("parse Git worktrees: %w", err)
	}
	return records, nil
}

// ListRepositoryFiles returns tracked and untracked, non-ignored files in
// repository-relative form. Dependency-specific filtering belongs in the
// dependencies package; this method only normalizes, de-duplicates, and sorts
// Git's NUL-delimited listing.
func ListRepositoryFiles(ctx context.Context, cwd string, runner CommandRunner) ([]string, error) {
	return NewClient(runner).ListRepositoryFiles(ctx, cwd)
}

// ListRepositoryFiles returns tracked and untracked, non-ignored files in
// repository-relative form.
func (client Client) ListRepositoryFiles(ctx context.Context, cwd string) ([]string, error) {
	result, err := client.run(ctx, cwd, []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"})
	if err != nil {
		return nil, fmt.Errorf("list Git repository files: %w", err)
	}
	return normalizeRepositoryFiles(result.Stdout), nil
}

func parseWorktreeRecords(cwd, output string) ([]WorktreeRecord, error) {
	base, err := absoluteClean(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}

	records := make([]WorktreeRecord, 0)
	var current *WorktreeRecord
	appendCurrent := func() {
		if current != nil {
			records = append(records, *current)
			current = nil
		}
	}
	for _, field := range strings.Split(output, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			appendCurrent()
			worktreePath, pathErr := resolveInventoryPath(base, strings.TrimPrefix(field, "worktree "))
			if pathErr != nil {
				return nil, pathErr
			}
			current = &WorktreeRecord{Path: worktreePath, Branch: "(detached)"}
			continue
		}
		if current == nil {
			continue
		}
		switch {
		case strings.HasPrefix(field, "HEAD "):
			current.Head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch "):
			current.Branch = strings.TrimPrefix(field, "branch ")
			current.Branch = strings.TrimPrefix(current.Branch, "refs/heads/")
		case field == "detached":
			current.Branch = "(detached)"
		}
	}
	appendCurrent()
	return records, nil
}

func resolveInventoryPath(base, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("Git returned an empty worktree path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}
	return absoluteClean(value)
}

func normalizeRepositoryFiles(output string) []string {
	seen := make(map[string]struct{})
	files := make([]string, 0)
	for _, value := range strings.Split(output, "\x00") {
		if value == "" {
			continue
		}
		normalized := strings.ReplaceAll(value, `\`, "/")
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		files = append(files, normalized)
	}
	sort.Strings(files)
	return files
}
