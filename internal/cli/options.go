package cli

import (
	"context"
	"io"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
	updatepkg "github.com/xenoviz/ruk/internal/update"
)

// Options configures an Application.
type Options struct {
	Version            string
	Distribution       updatepkg.Distribution
	Entrypoint         string
	Stdout             io.Writer
	Stderr             io.Writer
	Stdin              io.Reader
	Update             UpdateOperation
	Renew              RepositoryRenewOperation
	Sync               SyncRouteOperation
	Create             CreateRouteOperation
	Acquire            AcquireRouteOperation
	Release            ReleaseRouteOperation
	Remove             RemoveRouteOperation
	Warm               WarmRouteOperation
	GC                 GCRouteOperation
	Run                RunRouteOperation
	Exec               ExecRouteOperation
	Shell              ShellRouteOperation
	CWD                string
	DiscoverRepository RepositoryDiscovery
	Queries            QueryDependencies
	Now                func() time.Time
}

// UpdateOperation is injected so compatibility tests can exercise CLI output
// without network access or executable replacement.
type UpdateOperation func(context.Context, updatepkg.Options) (updatepkg.Result, error)

// RepositoryRenewOperation performs one lifecycle renewal in the discovered
// repository while keeping state composition out of compatibility tests.
type RepositoryRenewOperation func(context.Context, git.Repository, string, time.Time) (state.WorkspaceRecord, error)

// SyncRouteOperation executes init/sync after repository discovery. The
// operation owns rendering when SyncCommandInput.Emit is true.
type SyncRouteOperation func(context.Context, SyncCommandInput) (SyncCommandResult, error)

// CreateRouteOperation executes create with the discovered repository. The
// input carries stdout so the service emits its result exactly once.
type CreateRouteOperation func(context.Context, CreateCommandInput) (CreateCommandResult, error)

// AcquireRouteOperation executes acquire against one discovered repository.
type AcquireRouteOperation func(context.Context, git.Repository, AcquireInput) (AcquireResult, error)

// ReleaseRouteOperation executes release and returns its rendered result.
type ReleaseRouteOperation func(context.Context, ReleaseInput) (ReleaseResult, error)

// RemoveRouteOperation performs remove, which has no success output.
type RemoveRouteOperation func(context.Context, RemoveInput) error

// WarmRouteOperation executes warm after repository discovery. Warm validates
// the public input before invoking this seam with a typed request.
type WarmRouteOperation func(context.Context, git.Repository, WarmRequest) (lifecycle.WarmResult, error)

// GCRouteOperation executes garbage collection after repository discovery. GC
// validates options and computes its cutoff before invoking this seam.
type GCRouteOperation func(context.Context, git.Repository, GCRequest) (lifecycle.GCResult, error)

// RunRouteInput contains the discovered repository and validated run options.
// The route returns the child exit code directly; it does not render success.
type RunRouteInput struct {
	Repository          git.Repository
	CWD                 string
	Command             []string
	AllowSharedCheckout bool
	Stderr              io.Writer
	Now                 time.Time
}

// RunRouteOperation executes run with one discovered repository.
type RunRouteOperation func(context.Context, RunRouteInput) (int, error)

// ExecRouteInput contains the discovered repository, acquisition options, and
// command for exec. AcquireInput carries the validated lease/start-point
// options and the application clock without reparsing CLI arguments.
type ExecRouteInput struct {
	Repository          git.Repository
	CWD                 string
	Acquire             AcquireInput
	Command             []string
	AllowSharedCheckout bool
	Now                 time.Time
}

// ExecRouteOperation executes exec and returns the child exit code directly.
type ExecRouteOperation func(context.Context, ExecRouteInput) (int, error)

// ShellRouteInput contains the discovered repository, validated acquisition
// options, and the application's interactive stdio streams.
type ShellRouteInput struct {
	Repository git.Repository
	CWD        string
	Branch     string
	From       string
	Fetch      bool
	TTL        string
	Owner      string
	Ports      []string
	Now        time.Time
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

// ShellRouteOperation executes one interactive managed shell and returns its
// exact terminal/release result.
type ShellRouteOperation func(context.Context, ShellRouteInput) (ShellResult, error)

// RepositoryDiscovery resolves the current checkout without coupling the
// command router to Git subprocesses in compatibility tests.
type RepositoryDiscovery func(context.Context, string) (git.Repository, error)
