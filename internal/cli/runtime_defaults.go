package cli

import (
	"context"
	"os"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/lifecycle"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

// RuntimeDefaults is the production route bundle for pool maintenance and
// command execution. Options returns an Application-compatible projection.
type RuntimeDefaults struct {
	Mutations MutationAdapters
	Now       func() time.Time
	Warm      WarmRouteOperation
	GC        GCRouteOperation
	Run       RunRouteOperation
	Exec      ExecRouteOperation
	Shell     ShellRouteOperation
}

// Options returns all production mutation and runtime routes for cli.New.
func (defaults RuntimeDefaults) Options() Options {
	return Options{
		Sync: defaults.Mutations.Sync, Create: defaults.Mutations.Create,
		Acquire: defaults.Mutations.Acquire, Release: defaults.Mutations.Release,
		Remove: defaults.Mutations.Remove, Warm: defaults.Warm, GC: defaults.GC,
		Run: defaults.Run, Exec: defaults.Exec, Shell: defaults.Shell, Now: defaults.Now,
	}
}

// RuntimeDefaultsOptions supplies low-level seams for deterministic embedding
// and tests. Nil values select native implementations.
type RuntimeDefaultsOptions struct {
	Now       func() time.Time
	NewID     func() string
	Mutations MutationAdapterOptions
	GitRunner git.CommandRunner

	WarmWorkspace          func(git.Repository) (lifecycle.WarmWorkspaceService, error)
	WarmHeads              func(context.Context, git.Repository) (map[string]string, error)
	WarmTargetHead         func(context.Context, git.Repository, string, bool) (string, error)
	WarmValidateDependency func(context.Context, git.Repository, string, state.TreeRecord) (bool, error)
	GCWorkspace            func(git.Repository) (lifecycle.GCWorkspaceGit, error)

	ExecuteRunner        processpkg.Runner
	ExecuteActivity      ExecuteActivityRunner
	ExecuteSignals       func() (<-chan os.Signal, func())
	ShellTerminal        ShellTerminal
	PrimaryCheckoutFence PrimaryCheckoutFence
}

type runtimeExecutionOwnership uint8

const (
	runtimeExecutionOwnershipUnknown runtimeExecutionOwnership = iota
	runtimeExecutionOwnershipRetained
	runtimeExecutionOwnershipReleased
)

// NewRuntimeDefaults constructs fail-closed production routes. It does not
// discover a repository or mutate state until one of the returned routes runs.
func NewRuntimeDefaults(options RuntimeDefaultsOptions) (RuntimeDefaults, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomMutationID
	}
	mutationOptions := options.Mutations
	if mutationOptions.Now == nil {
		mutationOptions.Now = now
	}
	if mutationOptions.NewID == nil {
		mutationOptions.NewID = newID
	}
	options.Mutations = mutationOptions
	mutations, err := NewMutationAdapters(mutationOptions)
	if err != nil {
		return RuntimeDefaults{}, err
	}
	defaults := RuntimeDefaults{Mutations: mutations, Now: now}
	defaults.Warm = func(ctx context.Context, repository git.Repository, request WarmRequest) (lifecycle.WarmResult, error) {
		return runtimeWarm(ctx, repository, request, now, newID, mutations.Sync, options)
	}
	defaults.GC = func(ctx context.Context, repository git.Repository, request GCRequest) (lifecycle.GCResult, error) {
		return runtimeGC(ctx, repository, request.Options, now, newID, options)
	}
	defaults.Run = func(ctx context.Context, input RunRouteInput) (int, error) {
		return runtimeRun(ctx, input, now, newID, mutations.Sync, options)
	}
	defaults.Exec = func(ctx context.Context, input ExecRouteInput) (int, error) {
		return runtimeExec(ctx, input, now, newID, mutations, options)
	}
	defaults.Shell = func(ctx context.Context, input ShellRouteInput) (ShellResult, error) {
		return runtimeShell(ctx, input, now, newID, mutations, options)
	}
	return defaults, nil
}
