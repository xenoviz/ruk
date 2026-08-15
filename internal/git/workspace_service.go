package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceFileSystem is the filesystem seam used when authorizing a
// worktree path. Evaluating links matters here: a lexical path below the
// managed root can still point outside it through a symlink.
type WorkspaceFileSystem interface {
	EvalSymlinks(string) (string, error)
}

// OSWorkspaceFileSystem is the production filesystem implementation.
type OSWorkspaceFileSystem struct{}

func (OSWorkspaceFileSystem) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// WorkspaceServiceOptions configures a bounded Git worktree service.
// ManagedRoot is the only root under which the service may mutate worktrees.
type WorkspaceServiceOptions struct {
	RepositoryRoot string
	ManagedRoot    string
	Runner         CommandRunner
	Files          WorkspaceFileSystem
}

// WorkspaceService owns the mutating Git operations used by the workspace
// lifecycle. It deliberately exposes only operations needed by lifecycle
// callers; repository discovery and remote/ref operations remain on Client.
type WorkspaceService struct {
	repositoryRoot string
	managedRoot    string
	git            Client
	files          WorkspaceFileSystem
}

// NewWorkspaceService constructs a service with fail-closed path checking.
// ManagedRoot may be relative, but is normalized to an absolute path before
// any operation is attempted.
func NewWorkspaceService(options WorkspaceServiceOptions) (*WorkspaceService, error) {
	if strings.TrimSpace(options.RepositoryRoot) == "" {
		return nil, errors.New("repository root must not be empty")
	}
	if strings.TrimSpace(options.ManagedRoot) == "" {
		return nil, errors.New("managed repository root must not be empty")
	}
	repositoryRoot, err := absoluteClean(options.RepositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	managedRoot, err := absoluteClean(options.ManagedRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve managed repository root: %w", err)
	}
	files := options.Files
	if files == nil {
		files = OSWorkspaceFileSystem{}
	}
	return &WorkspaceService{repositoryRoot: repositoryRoot, managedRoot: managedRoot, git: NewClient(options.Runner), files: files}, nil
}

// NewWorkspaceServiceAt is a convenience constructor for callers that keep
// the repository root and injected seams as separate values.
func NewWorkspaceServiceAt(repositoryRoot, managedRoot string, runner CommandRunner, files WorkspaceFileSystem) (*WorkspaceService, error) {
	return NewWorkspaceService(WorkspaceServiceOptions{RepositoryRoot: repositoryRoot, ManagedRoot: managedRoot, Runner: runner, Files: files})
}

// ManagedRoot returns the normalized root used for path authorization.
func (service *WorkspaceService) ManagedRoot() string {
	if service == nil {
		return ""
	}
	return service.managedRoot
}

// RepositoryRoot returns the normalized checkout used as Git's repository
// context for worktree management commands.
func (service *WorkspaceService) RepositoryRoot() string {
	if service == nil {
		return ""
	}
	return service.repositoryRoot
}

// Create adds a worktree below the managed root. Existing local branches are
// reused; missing branches are created from startPoint. Detached creation does
// not create a local branch.
func (service *WorkspaceService) Create(ctx context.Context, destination, branch, startPoint string, detach bool) error {
	if err := service.validateDestination(destination); err != nil {
		return err
	}
	if err := service.configured(); err != nil {
		return err
	}
	return service.git.AddWorktree(ctx, service.repositoryRoot, destination, branch, startPoint, detach)
}

// Add is the name used by callers that mirror the Git operation.
func (service *WorkspaceService) Add(ctx context.Context, destination, branch, startPoint string, detach bool) error {
	return service.Create(ctx, destination, branch, startPoint, detach)
}

// Assign attaches an existing pooled worktree to the requested branch. The
// destination is authorized against ManagedRoot before Git sees it.
func (service *WorkspaceService) Assign(ctx context.Context, destination, branch, startPoint string) error {
	if err := service.validateDestination(destination); err != nil {
		return err
	}
	if err := service.configured(); err != nil {
		return err
	}
	return service.git.AssignWorktree(ctx, service.repositoryRoot, destination, branch, startPoint)
}

// Return resets and cleans a managed worktree, then detaches it for reuse.
// Non-forced returns refuse dirty worktrees; forced returns hard-reset tracked
// changes before removing ignored and untracked files.
func (service *WorkspaceService) Return(ctx context.Context, destination string, force bool, preservedProjections []string) error {
	if err := service.validateDestination(destination); err != nil {
		return err
	}
	if err := service.configured(); err != nil {
		return err
	}
	return service.git.ReturnWorktree(ctx, destination, force, preservedProjections)
}

// ResetCleanReturn is an explicit alias for Return for lifecycle callers.
func (service *WorkspaceService) ResetCleanReturn(ctx context.Context, destination string, force bool, preservedProjections []string) error {
	return service.Return(ctx, destination, force, preservedProjections)
}

// Lock protects a pooled worktree from ordinary Git maintenance.
func (service *WorkspaceService) Lock(ctx context.Context, destination string) error {
	if err := service.validateDestination(destination); err != nil {
		return err
	}
	if err := service.configured(); err != nil {
		return err
	}
	return service.git.LockWorktree(ctx, service.repositoryRoot, destination)
}

// Unlock permits cleanup of pooled capacity. Already-unlocked worktrees are an
// idempotent success, while unexpected Git failures remain visible.
func (service *WorkspaceService) Unlock(ctx context.Context, destination string) error {
	if err := service.validateDestination(destination); err != nil {
		return err
	}
	if err := service.configured(); err != nil {
		return err
	}
	return service.git.UnlockWorktree(ctx, service.repositoryRoot, destination)
}

// SafeRemove unlocks and removes a worktree. If removal fails, it restores the
// lock before returning so a failed cleanup cannot publish an unlocked slot.
func (service *WorkspaceService) SafeRemove(ctx context.Context, destination string, force bool) error {
	if err := service.validateDestination(destination); err != nil {
		return err
	}
	if err := service.configured(); err != nil {
		return err
	}
	if err := service.git.UnlockWorktree(ctx, service.repositoryRoot, destination); err != nil {
		return err
	}
	if err := service.git.RemoveWorktree(ctx, service.repositoryRoot, destination, force); err != nil {
		relockErr := service.git.LockWorktree(ctx, service.repositoryRoot, destination)
		if relockErr != nil {
			return errors.Join(err, fmt.Errorf("relock worktree %s after failed removal: %w", destination, relockErr))
		}
		return err
	}
	return nil
}

// Remove is the safe removal operation used by lifecycle cleanup.
func (service *WorkspaceService) Remove(ctx context.Context, destination string, force bool) error {
	return service.SafeRemove(ctx, destination, force)
}

// RemoveAndPrune removes a managed worktree and then asks Git to prune stale
// worktree metadata. A prune failure is returned; the successful removal is
// not falsely reported as reversible.
func (service *WorkspaceService) RemoveAndPrune(ctx context.Context, destination string, force bool) error {
	if err := service.SafeRemove(ctx, destination, force); err != nil {
		return err
	}
	return service.Prune(ctx)
}

// Prune removes stale Git worktree metadata for the managed repository.
func (service *WorkspaceService) Prune(ctx context.Context) error {
	if err := service.configured(); err != nil {
		return err
	}
	if _, err := service.git.run(ctx, service.repositoryRoot, []string{"worktree", "prune"}); err != nil {
		return fmt.Errorf("prune Git worktrees: %w", err)
	}
	return nil
}

func (service *WorkspaceService) configured() error {
	if service == nil || service.git.Runner == nil || service.files == nil || service.repositoryRoot == "" || service.managedRoot == "" {
		return errors.New("Git workspace service is not configured")
	}
	return nil
}

func (service *WorkspaceService) validateDestination(destination string) error {
	if err := service.configured(); err != nil {
		return err
	}
	if strings.TrimSpace(destination) == "" {
		return errors.New("worktree destination must not be empty")
	}
	resolved, err := absoluteClean(destination)
	if err != nil {
		return fmt.Errorf("resolve worktree destination: %w", err)
	}
	configuredRoot := service.managedRoot
	if !pathWithin(configuredRoot, resolved) || samePath(configuredRoot, resolved) {
		return fmt.Errorf("worktree destination %s is outside managed repository root %s", resolved, configuredRoot)
	}
	root, err := service.canonicalPath(configuredRoot)
	if err != nil {
		return fmt.Errorf("validate managed repository root: %w", err)
	}
	canonical, err := service.canonicalExistingPath(resolved)
	if err != nil {
		return fmt.Errorf("validate worktree destination %s: %w", resolved, err)
	}
	if !pathWithin(root, canonical) || samePath(root, canonical) {
		return fmt.Errorf("worktree destination %s is outside managed repository root %s", resolved, root)
	}
	return nil
}

func (service *WorkspaceService) canonicalPath(path string) (string, error) {
	canonical, err := service.files.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(canonical) == "" {
		return "", errors.New("filesystem returned an empty path")
	}
	return absoluteClean(canonical)
}

// canonicalExistingPath evaluates the destination or its nearest existing
// ancestor. Git add accepts a not-yet-created destination, so absence itself is
// not unsafe; an unreadable ancestor is unsafe and fails closed.
func (service *WorkspaceService) canonicalExistingPath(path string) (string, error) {
	original := path
	candidate := path
	for {
		canonical, err := service.files.EvalSymlinks(candidate)
		if err == nil {
			canonical, canonicalErr := absoluteClean(canonical)
			if canonicalErr != nil {
				return "", canonicalErr
			}
			suffix, suffixErr := filepath.Rel(candidate, original)
			if suffixErr != nil {
				return "", suffixErr
			}
			return absoluteClean(filepath.Join(canonical, suffix))
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if samePath(parent, candidate) {
			return "", err
		}
		candidate = parent
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
