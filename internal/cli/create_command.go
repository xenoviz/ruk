package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/git"
)

// CreateWorkspace performs the Git worktree mutation needed by create. The
// interface is intentionally narrower than git.WorkspaceService so tests can
// exercise command cleanup without a real repository.
type CreateWorkspace interface {
	Create(context.Context, string, string, string, bool) error
	Remove(context.Context, string, bool) error
}

// CreateStartPointResolver resolves --from and --fetch. The resolver owns all
// remote validation and fetching; create only preserves the resulting ref.
type CreateStartPointResolver func(context.Context, git.Repository, string, bool) (string, error)

// CreateSyncRequest supplies the destination context to the shared dependency
// synchronization seam. Output is a progress sink; create owns the final
// record so it can guarantee one JSON value and the correct destination.
type CreateSyncRequest struct {
	Repository git.Repository
	JSON       bool
	Output     io.Writer
}

// CreateSyncOperation prepares one newly created worktree. Callers normally
// adapt SyncCommand.Run here, supplying the discovered destination context and
// repository configuration they already own.
type CreateSyncOperation func(context.Context, CreateSyncRequest) (SyncCommandResult, error)

// CreateLifecycleFence serializes worktree creation, preparation, and failure
// cleanup with lifecycle operations for the destination. A nil fence runs the
// operation directly.
type CreateLifecycleFence func(context.Context, string, func() error) error

// CreateCommand composes the create workflow while leaving Git, dependency,
// repository discovery, and lifecycle state ownership behind injected seams.
type CreateCommand struct {
	Workspace  CreateWorkspace
	StartPoint CreateStartPointResolver
	Sync       CreateSyncOperation
	Fence      CreateLifecycleFence
}

const createRecoveryTimeout = 30 * time.Second

// CreateCommandOptions configures the injected create seams.
type CreateCommandOptions struct {
	Workspace  CreateWorkspace
	StartPoint CreateStartPointResolver
	Sync       CreateSyncOperation
	Fence      CreateLifecycleFence
}

// NewCreateCommand constructs a create service from its injected Git,
// synchronization, and lifecycle seams.
func NewCreateCommand(options CreateCommandOptions) *CreateCommand {
	startPoint := options.StartPoint
	if startPoint == nil {
		startPoint = defaultCreateStartPoint
	}
	return &CreateCommand{
		Workspace: options.Workspace, StartPoint: startPoint,
		Sync: options.Sync, Fence: options.Fence,
	}
}

// CreateCommandInput is the parsed create request plus the repository context
// supplied by the command router. Path is interpreted relative to CWD, as in
// the TypeScript CLI; an empty Path selects the documented default.
type CreateCommandInput struct {
	Repository git.Repository
	CWD        string
	Branch     string
	Path       string
	From       string
	Fetch      bool
	Detach     bool
	JSON       bool
	Output     io.Writer
}

// CreateInput is a short compatibility alias for callers that use command
// names as input types.
type CreateInput = CreateCommandInput

// CreateCommandResult preserves the dependency result while adding the
// destination and rendered output. Output is excluded from JSON because the
// sync result is the machine-readable record emitted to the supplied writer.
type CreateCommandResult struct {
	Status          string `json:"status"`
	Path            string `json:"path"`
	Fingerprint     string `json:"fingerprint"`
	Mode            string `json:"mode"`
	Reused          bool   `json:"reused"`
	AlreadyAttached bool   `json:"alreadyAttached"`
	Output          string `json:"-"`
}

// Result is an alias retained for concise command-router integrations.
type CreateResult = CreateCommandResult

// Run creates and prepares one ordinary Git worktree. It does not record a
// managed assignment. If preparation fails after Git creation, the worktree is
// force-removed; cleanup errors are joined with the original failure.
func (command *CreateCommand) Run(ctx context.Context, input CreateCommandInput) (CreateCommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CreateCommandResult{}, err
	}
	if command == nil || command.Workspace == nil {
		return CreateCommandResult{}, errors.New("create workspace service is not configured")
	}
	if command.StartPoint == nil {
		command.StartPoint = defaultCreateStartPoint
	}
	if command.Sync == nil {
		return CreateCommandResult{}, errors.New("create synchronization service is not configured")
	}
	if strings.TrimSpace(input.Repository.Root) == "" {
		return CreateCommandResult{}, errors.New("repository root must not be empty")
	}
	if strings.TrimSpace(input.Branch) == "" {
		return CreateCommandResult{}, errors.New("branch must not be empty")
	}
	cwd := input.CWD
	if cwd == "" {
		cwd = input.Repository.Root
	}
	cwd, err := filepath.Abs(filepath.Clean(cwd))
	if err != nil {
		return CreateCommandResult{}, fmt.Errorf("resolve create working directory: %w", err)
	}
	destination, err := createDestination(cwd, input.Repository.Root, input.Branch, input.Path)
	if err != nil {
		return CreateCommandResult{}, err
	}
	startPoint, err := command.StartPoint(ctx, input.Repository, input.From, input.Fetch)
	if err != nil {
		return CreateCommandResult{}, err
	}

	var result CreateCommandResult
	operation := func() error {
		if err := command.Workspace.Create(ctx, destination, input.Branch, startPoint, input.Detach); err != nil {
			return err
		}
		cleanupAfterFailure := func(cause error) error {
			// Preparation cleanup must still run when the operation was canceled,
			// but it must not be allowed to wait indefinitely for a broken Git
			// operation. Keep the original cause as the returned error below.
			cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), createRecoveryTimeout)
			cleanupErr := command.Workspace.Remove(cleanupCtx, destination, true)
			cancelCleanup()
			if cleanupErr != nil {
				return fmt.Errorf("workspace preparation failed and cleanup also failed for %s: %w", destination, errors.Join(cause, cleanupErr))
			}
			return cause
		}
		syncResult, syncErr := command.Sync(ctx, CreateSyncRequest{
			Repository: destinationRepository(input.Repository, destination),
			JSON:       input.JSON,
			Output:     io.Discard,
		})
		if syncErr != nil {
			return cleanupAfterFailure(syncErr)
		}
		status := syncResult.Status
		if status == "" {
			status = "prepared"
			if syncResult.AlreadyAttached {
				status = "ready"
			}
		}

		result = CreateCommandResult{
			Status:          status,
			Path:            destination,
			Fingerprint:     syncResult.Fingerprint,
			Mode:            syncResult.Mode,
			Reused:          syncResult.Reused,
			AlreadyAttached: syncResult.AlreadyAttached,
		}
		if input.JSON {
			// Create owns the final record so the path is always the newly-created
			// destination, even when the injected sync seam is silent.
			var output bytes.Buffer
			if err := json.NewEncoder(&output).Encode(struct {
				Status      string `json:"status"`
				Fingerprint string `json:"fingerprint"`
				Mode        string `json:"mode"`
				Path        string `json:"path"`
			}{
				Status: status, Fingerprint: result.Fingerprint, Mode: result.Mode, Path: result.Path,
			}); err != nil {
				return fmt.Errorf("encode create result: %w", err)
			}
			result.Output = output.String()
			if input.Output != nil {
				if _, err := io.WriteString(input.Output, result.Output); err != nil {
					return fmt.Errorf("write create result: %w", err)
				}
			}
			return nil
		}
		fingerprint := syncResult.Fingerprint
		if len(fingerprint) > 12 {
			fingerprint = fingerprint[:12]
		}
		verb := "Dependencies prepared"
		if syncResult.AlreadyAttached {
			verb = "Dependencies already ready"
		}
		result.Output = fmt.Sprintf("%s for %s (%s).\n%s\n", verb, fingerprint, syncResult.Mode, destination)
		if input.Output != nil {
			if _, err := io.WriteString(input.Output, result.Output); err != nil {
				return fmt.Errorf("write create result: %w", err)
			}
		}
		return nil
	}

	if command.Fence != nil {
		err = command.Fence(ctx, destination, operation)
	} else {
		err = operation()
	}
	if err != nil {
		return CreateCommandResult{}, err
	}
	return result, nil
}

// Execute is an explicit synonym for Run for routers with uniform command
// method names.
func (command *CreateCommand) Execute(ctx context.Context, input CreateCommandInput) (CreateCommandResult, error) {
	return command.Run(ctx, input)
}

func createDestination(cwd, repositoryRoot, branch, requested string) (string, error) {
	value := requested
	if value == "" {
		value = filepath.Join(filepath.Dir(repositoryRoot), filepath.Base(repositoryRoot)+"-"+createSlug(branch))
	}
	pathValue := value
	if !filepath.IsAbs(pathValue) {
		pathValue = filepath.Join(cwd, pathValue)
	}
	destination, err := filepath.Abs(pathValue)
	if err != nil {
		return "", fmt.Errorf("resolve create destination: %w", err)
	}
	return filepath.Clean(destination), nil
}

func createSlug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	value = strings.Trim(builder.String(), "-")
	if value == "" {
		return "workspace"
	}
	return value
}

func destinationRepository(repository git.Repository, destination string) git.Repository {
	repository.Root = destination
	repository.PrimaryCheckout = false
	return repository
}

func defaultCreateStartPoint(ctx context.Context, repository git.Repository, requested string, fetch bool) (string, error) {
	client := git.NewClient(nil)
	if requested != "" {
		if fetch {
			remote, err := client.SelectRemote(ctx, repository.Root, requested)
			if err != nil {
				return "", err
			}
			if err := client.Fetch(ctx, repository.Root, remote, requested); err != nil {
				return "", err
			}
		}
		return requested, nil
	}
	if !fetch {
		return "HEAD", nil
	}
	return client.FetchDefaultBranch(ctx, repository.Root)
}
