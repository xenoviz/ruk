package cli

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

// RemoveInput contains the discovered repository and user-selected worktree.
type RemoveInput struct {
	Repository git.Repository
	CWD        string
	Path       string
	Force      bool
}

// RemoveCommand keeps unmanaged removal behind injected Git, state, and lock
// boundaries. Managed pool members must use their fenced lifecycle commands.
type RemoveCommand struct {
	Canonicalize func(string) (string, error)
	ReadState    func(context.Context, string) (state.State, error)
	WithLock     func(context.Context, string, func() error) error
	LockPath     func(string, string) string
	Remove       func(context.Context, string, string, bool) error
	DeleteTree   func(context.Context, string, string) error
}

// Run removes one unmanaged non-current worktree and its preparation record.
func (command RemoveCommand) Run(ctx context.Context, input RemoveInput) error {
	if input.Repository.Root == "" || input.Repository.CommonDir == "" {
		return errors.New("repository context is incomplete")
	}
	if input.Path == "" {
		return errors.New("workspace path must not be empty")
	}
	if command.Canonicalize == nil || command.ReadState == nil {
		return errors.New("remove command inspection is not configured")
	}
	cwd := input.CWD
	if cwd == "" {
		cwd = input.Repository.Root
	}
	destinationInput := input.Path
	if !filepath.IsAbs(destinationInput) {
		destinationInput = filepath.Join(cwd, destinationInput)
	}
	destination, err := command.Canonicalize(destinationInput)
	if err != nil {
		return err
	}
	if sameQueryPath(destination, input.Repository.Root) {
		return errors.New("Refusing to remove the current workspace")
	}
	snapshot, err := command.ReadState(ctx, input.Repository.CommonDir)
	if err != nil {
		return err
	}
	key, err := state.TreeKey(destination)
	if err != nil {
		return err
	}
	if managed, exists := snapshot.Workspaces[key]; exists {
		if managed.Assignment != nil {
			return errors.New("Workspace is managed by assignment " + managed.Assignment.ID + "; use ruk release " + managed.Assignment.ID)
		}
		return errors.New("Workspace belongs to the managed pool; use ruk gc --apply")
	}
	if command.WithLock == nil || command.LockPath == nil || command.Remove == nil || command.DeleteTree == nil {
		return errors.New("remove command mutation is not configured")
	}
	lockPath := command.LockPath(input.Repository.CommonDir, destination)
	if lockPath == "" {
		return errors.New("remove command lock path is empty")
	}
	return command.WithLock(ctx, lockPath, func() error {
		if err := command.Remove(ctx, input.Repository.Root, destination, input.Force); err != nil {
			return err
		}
		return command.DeleteTree(ctx, input.Repository.CommonDir, destination)
	})
}
