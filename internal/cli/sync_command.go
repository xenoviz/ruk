package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/xenoviz/ruk/internal/config"
	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/git"
)

// SyncManagerResolver selects the effective dependency manager for a
// repository. It is injected so command composition does not own manager
// detection or package-manager probing.
type SyncManagerResolver func(context.Context, string, config.Config) (dependencies.PackageManager, error)

// SyncRepositoryFileLister returns the repository inventory used by the
// dependency fingerprint. Git remains the authority for this inventory.
type SyncRepositoryFileLister func(context.Context, string) ([]string, error)

// SyncEnsureOperation is the dependency package's orchestration seam. Keeping
// it at the command boundary lets command tests exercise output and guard
// behavior without running an installer or writing state.
type SyncEnsureOperation func(context.Context, dependencies.EnsureInput) (dependencies.EnsureResult, error)

// SharedCheckoutGuard is supplied by the caller that owns repository state and
// the primary-checkout fence. It is called only for guarded commands that did
// not opt into --allow-shared-checkout.
type SharedCheckoutGuard func(context.Context, git.Repository, config.Config) error

// SyncCommand composes repository dependency synchronization. It deliberately
// does not discover a repository or load configuration; those facts are
// supplied by the command router so init and sync can share this service.
type SyncCommand struct {
	ResolveManager SyncManagerResolver
	ListFiles      SyncRepositoryFileLister
	Ensure         SyncEnsureOperation
	Guard          SharedCheckoutGuard
}

// NewSyncCommand returns a production command with the repository and
// dependency package defaults wired in. The guard is intentionally nil: the
// CLI composition layer must provide the state-aware primary-checkout guard.
func NewSyncCommand() SyncCommand {
	return SyncCommand{
		ResolveManager: func(ctx context.Context, root string, cfg config.Config) (dependencies.PackageManager, error) {
			return dependencies.ResolvePackageManager(ctx, root, cfg)
		},
		ListFiles: func(ctx context.Context, root string) ([]string, error) {
			return git.ListRepositoryFiles(ctx, root, nil)
		},
		Ensure: dependencies.EnsureDependencies,
	}
}

// SyncCommandInput contains the repository context and command policy. The
// Ensure field is an optional template for callers that need to inject a
// state store, preparation lock, installer, or ownership check; the service
// always replaces its repository, manager, and file inventory fields.
type SyncCommandInput struct {
	Repository git.Repository
	Config     config.Config
	Ensure     dependencies.EnsureInput

	JSON                bool
	Emit                bool
	GuardSharedCheckout bool
	AllowSharedCheckout bool
	Output              io.Writer
}

// SyncCommandResult is the stable sync result. Status and path are the
// command-facing fields; the dependency fields retain the complete result
// returned by dependencies.EnsureDependencies for init/sync callers and JSON
// consumers.
type SyncCommandResult struct {
	Status          string `json:"status"`
	Path            string `json:"path"`
	Fingerprint     string `json:"fingerprint"`
	Mode            string `json:"mode"`
	Reused          bool   `json:"reused"`
	AlreadyAttached bool   `json:"alreadyAttached"`
}

// Run resolves the manager, inventories repository files, checks the optional
// shared-primary-checkout guard, and delegates preparation to the dependency
// package. If Emit is true it writes one final human or JSON record. JSON mode
// never writes human progress; callers should configure the installer/output
// layer to discard installer streams in that mode.
func (command SyncCommand) Run(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SyncCommandResult{}, err
	}
	if input.Repository.Root == "" {
		return SyncCommandResult{}, errors.New("repository root must not be empty")
	}
	if command.ResolveManager == nil {
		command.ResolveManager = NewSyncCommand().ResolveManager
	}
	if command.ListFiles == nil {
		command.ListFiles = NewSyncCommand().ListFiles
	}
	if command.Ensure == nil {
		command.Ensure = dependencies.EnsureDependencies
	}
	if input.GuardSharedCheckout && input.Repository.PrimaryCheckout && !input.AllowSharedCheckout && command.Guard != nil {
		if err := command.Guard(ctx, input.Repository, input.Config); err != nil {
			return SyncCommandResult{}, err
		}
	}

	manager, err := command.ResolveManager(ctx, input.Repository.Root, input.Config)
	if err != nil {
		return SyncCommandResult{}, err
	}
	files, err := command.ListFiles(ctx, input.Repository.Root)
	if err != nil {
		return SyncCommandResult{}, err
	}

	ensureInput := input.Ensure
	ensureInput.Repository = input.Repository
	ensureInput.Manager = manager
	ensureInput.Files = append([]string(nil), files...)
	// The command owns the initial inventory. Preserve a caller-provided lister
	// only when it explicitly requested a post-install rescan.
	result, err := command.Ensure(ctx, ensureInput)
	if err != nil {
		return SyncCommandResult{}, err
	}

	output := SyncCommandResult{
		Status:          "prepared",
		Path:            input.Repository.Root,
		Fingerprint:     result.Fingerprint,
		Mode:            result.Mode,
		Reused:          result.Reused,
		AlreadyAttached: result.AlreadyAttached,
	}
	if result.AlreadyAttached {
		output.Status = "ready"
	}
	if input.Emit {
		writer := input.Output
		if writer == nil {
			writer = io.Discard
		}
		if input.JSON {
			if err := json.NewEncoder(writer).Encode(output); err != nil {
				return SyncCommandResult{}, fmt.Errorf("write sync result: %w", err)
			}
		} else {
			verb := "Dependencies prepared"
			if result.AlreadyAttached {
				verb = "Dependencies already ready"
			}
			fingerprint := result.Fingerprint
			if len(fingerprint) > 12 {
				fingerprint = fingerprint[:12]
			}
			if _, err := fmt.Fprintf(writer, "%s for %s (%s).\n", verb, fingerprint, result.Mode); err != nil {
				return SyncCommandResult{}, fmt.Errorf("write sync result: %w", err)
			}
		}
	}
	return output, nil
}

// Execute is an explicit synonym for Run for routers that name command
// execution methods uniformly.
func (command SyncCommand) Execute(ctx context.Context, input SyncCommandInput) (SyncCommandResult, error) {
	return command.Run(ctx, input)
}
