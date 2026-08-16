package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/ports"
)

// ShellAcquirer is the acquire seam used by ShellService. It may delegate to
// Acquire or to an application-specific lifecycle adapter.
type ShellAcquirer func(context.Context, AcquireInput) (AcquireResult, error)

// ShellTerminalRequest contains the exact assignment and stdio handed to a
// PTY/session implementation. The terminal owns descendant tracking.
type ShellTerminalRequest struct {
	AssignmentID  string
	WorkspacePath string
	Shell         string
	Environment   map[string]string
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

// ShellTerminalResult reports terminal completion and whether all descendants
// have drained. ExitCode and Signal are preserved for the caller.
type ShellTerminalResult struct {
	ExitCode int
	Signal   os.Signal
	// Started is stage evidence from the process runner. False means startup
	// failed before a child existed, so the acquired assignment can be safely
	// released. True means ownership crossed the process boundary and any
	// uncertain cleanup must remain fenced for recovery.
	Started            bool
	DescendantsDrained bool
}

// ShellTerminal is the PTY/session seam. A production implementation still
// needs platform-specific terminal/session creation and identity tracking.
type ShellTerminal interface {
	Run(context.Context, ShellTerminalRequest) (ShellTerminalResult, error)
}

// ShellRelease releases exactly one assignment after terminal descendants are
// confirmed drained.
type ShellRelease func(context.Context, string) error

// ShellInput describes one high-level shell request.
type ShellInput struct {
	Branch           string
	From             string
	Fetch            bool
	TTL              string
	Owner            string
	Ports            []string
	Now              time.Time
	OwnerFallback    func() string
	Hostname         string
	HostnameFallback func() string
	Environment      map[string]string
	Platform         string
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
}

// ShellResult contains the terminal status and assignment handoff state.
type ShellResult struct {
	Shell        string
	ExitCode     int
	Signal       os.Signal
	AssignmentID string
	Path         string
	ExpiresAt    string
	Released     bool
	Retained     bool
}

// RetainedShellError is the stable human diagnostic for unsafe terminal or
// release cleanup. Its unwrap chain also exposes RetainedAssignmentError to
// existing machine-readable error classification.
type RetainedShellError struct {
	AssignmentID string
	Path         string
	ExpiresAt    string
	Cause        error
}

func (err *RetainedShellError) Error() string {
	if err == nil {
		return "workspace retained"
	}
	return fmt.Sprintf("Workspace retained: %s\nAssignment: %s\nExpires: %s\nReason: %s\nRelease: ruk release %s", err.Path, err.AssignmentID, err.ExpiresAt, errorDetail(err.Cause), err.AssignmentID)
}

func (err *RetainedShellError) Unwrap() error {
	if err == nil {
		return nil
	}
	return errors.Join(err.Cause, NewRetainedAssignmentError(err.AssignmentID, err.Path, err.ExpiresAt, err.Cause))
}

// ShellOptions configures ShellService.
type ShellOptions struct {
	Acquire  ShellAcquirer
	Terminal ShellTerminal
	Release  ShellRelease
}

// ShellService composes acquire, interactive terminal execution, and exact
// assignment release.
type ShellService struct {
	acquire  ShellAcquirer
	terminal ShellTerminal
	release  ShellRelease
}

// NewShellService constructs a shell command service.
func NewShellService(options ShellOptions) *ShellService {
	return &ShellService{acquire: options.Acquire, terminal: options.Terminal, release: options.Release}
}

// Shell validates acquire inputs, selects a shell, runs an interactive
// terminal, and releases only once the terminal reports descendants drained.
func (service *ShellService) Shell(ctx context.Context, input ShellInput) (ShellResult, error) {
	if service == nil {
		return ShellResult{}, errors.New("shell service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if input.Branch == "" {
		return ShellResult{}, errors.New("branch must not be empty")
	}
	if service.acquire == nil {
		return ShellResult{}, errors.New("shell acquire operation is not configured")
	}
	if service.terminal == nil {
		return ShellResult{}, errors.New("shell terminal is not configured")
	}
	if service.release == nil {
		return ShellResult{}, errors.New("shell release operation is not configured")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	if _, err := ParseFutureMinutes(input.TTL, defaultLeaseMinutes, "--ttl", now); err != nil {
		return ShellResult{}, err
	}
	owner := input.Owner
	if owner == "" {
		fallback := input.OwnerFallback
		if fallback == nil {
			fallback = defaultOwnerFallback
		}
		owner = fallback()
	}
	if owner == "" {
		return ShellResult{}, errors.New("owner must not be empty")
	}
	hostname := input.Hostname
	if hostname == "" {
		fallback := input.HostnameFallback
		if fallback == nil {
			fallback = defaultHostnameFallback
		}
		hostname = fallback()
	}
	if hostname == "" {
		return ShellResult{}, errors.New("hostname must not be empty")
	}
	shell, err := SelectShell(input.Environment, input.Platform)
	if err != nil {
		return ShellResult{}, err
	}
	acquired, err := service.acquire(ctx, AcquireInput{
		Branch: input.Branch, From: input.From, Fetch: input.Fetch, TTL: input.TTL,
		Owner: owner, Ports: append([]string(nil), input.Ports...), Now: now,
		OwnerFallback: input.OwnerFallback, Hostname: hostname, HostnameFallback: input.HostnameFallback,
	})
	if err != nil {
		return ShellResult{}, err
	}
	if acquired.AssignmentID == "" || acquired.Path == "" || acquired.ExpiresAt == "" {
		return ShellResult{}, errors.New("shell acquire returned an incomplete assignment")
	}
	base := ShellResult{Shell: shell, AssignmentID: acquired.AssignmentID, Path: acquired.Path, ExpiresAt: acquired.ExpiresAt}
	releaseBeforeSpawnFailure := func(cause error) (ShellResult, error) {
		if releaseErr := service.release(context.WithoutCancel(ctx), acquired.AssignmentID); releaseErr != nil {
			base.Retained = true
			return base, retainedShellError(acquired, errors.Join(cause, releaseErr))
		}
		base.Released = true
		return base, cause
	}
	environment, err := ports.BuildEnvironment(acquired.Ports)
	if err != nil {
		return releaseBeforeSpawnFailure(fmt.Errorf("build shell port environment: %w", err))
	}
	if input.Stderr != nil {
		if _, err := fmt.Fprintf(input.Stderr, "Shell workspace: %s\nAssignment: %s\n", acquired.Path, acquired.AssignmentID); err != nil {
			return releaseBeforeSpawnFailure(fmt.Errorf("write shell handoff: %w", err))
		}
	}
	terminal, terminalErr := service.terminal.Run(ctx, ShellTerminalRequest{
		AssignmentID: acquired.AssignmentID, WorkspacePath: acquired.Path, Shell: shell,
		Environment: environment,
		Stdin:       input.Stdin, Stdout: input.Stdout, Stderr: input.Stderr,
	})
	base.ExitCode = terminal.ExitCode
	base.Signal = terminal.Signal
	if terminalErr != nil {
		if !terminal.Started {
			return releaseBeforeSpawnFailure(terminalErr)
		}
		base.Retained = true
		return base, retainedShellError(acquired, terminalErr)
	}
	if !terminal.DescendantsDrained {
		base.Retained = true
		return base, retainedShellError(acquired, errors.New("terminal descendants are still running"))
	}
	if err := service.release(context.WithoutCancel(ctx), acquired.AssignmentID); err != nil {
		base.Retained = true
		return base, retainedShellError(acquired, err)
	}
	base.Released = true
	return base, nil
}

// SelectShell applies the documented environment precedence and platform
// fallback without assuming util-linux script or another PTY helper.
func SelectShell(environment map[string]string, platform string) (string, error) {
	for _, name := range []string{"RUK_SHELL", "SHELL", "COMSPEC"} {
		if value := strings.TrimSpace(environment[name]); value != "" {
			return value, nil
		}
	}
	if platform == "" {
		platform = runtime.GOOS
	}
	if platform == "windows" {
		return "cmd.exe", nil
	}
	return "/bin/sh", nil
}

func retainedShellError(acquired AcquireResult, cause error) error {
	return &RetainedShellError{AssignmentID: acquired.AssignmentID, Path: acquired.Path, ExpiresAt: acquired.ExpiresAt, Cause: cause}
}
