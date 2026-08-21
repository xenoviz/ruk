package process

import (
	"context"
	"io"
	"os"

	"github.com/xenoviz/ruk/internal/state"
)

// ProcessMode describes whether a managed child is attached to the caller or
// owns a separately signalable process group.
type ProcessMode uint8

const (
	Attached ProcessMode = iota
	Detached
)

// SpawnRequest is the small, platform-neutral contract used by Runner.
type SpawnRequest struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
	Mode    ProcessMode
	// ForegroundTerminal asks a detached POSIX child to become the foreground
	// process group of a detected controlling terminal. Windows ignores this
	// flag because its Job Object remains the process-tree boundary.
	ForegroundTerminal bool
	Stdin              io.Reader
	Stdout             io.Writer
	Stderr             io.Writer
}

// ExitStatus is the result returned by a spawned process. Code is normalized
// by Runner to the conventional 128+signal value when Signal is non-nil.
type ExitStatus struct {
	Code          int
	Signal        os.Signal
	Err           error
	BoundaryError error
}

// Child is the minimal operating-system process boundary needed by Runner.
type Child interface {
	PID() int
	Wait() ExitStatus
	Signal(os.Signal) error
}

// Spawner starts one child with context-aware launch semantics.
type Spawner interface {
	Spawn(context.Context, SpawnRequest) (Child, error)
}

// ProcessDescriber turns a just-created child into an exact durable record.
// Implementations must fail when the leader identity cannot be established.
type ProcessDescriber interface {
	Describe(context.Context, int, ProcessMode, []string) (state.TrackedProcessRecord, error)
}

// RegisterFunc persists a process record before the runner hands control back
// to its caller. Registration errors trigger fail-closed cleanup.
type RegisterFunc func(context.Context, state.TrackedProcessRecord) error

// ProcessCleaner is used only when registration fails. It must retain the
// process when identity or group membership cannot be verified.
type ProcessCleaner interface {
	Cleanup(context.Context, Child, state.TrackedProcessRecord) error
}

// ProcessUnknownCleaner is an optional native boundary for a child whose
// durable identity could not be described. Drained is true only when the
// implementation owns a stronger exact boundary (for example a Windows Job
// Object) and has terminated the complete tree.
type ProcessUnknownCleaner interface {
	CleanupUnknown(context.Context, Child, ProcessMode, state.TrackedProcessRecord) (drained bool, err error)
}

// ProcessCleanupVerifier proves that a previously cleaned detached tree has
// fully drained. Signaling a group does not itself wait for descendants.
type ProcessCleanupVerifier interface {
	Exists(context.Context, state.TrackedProcessRecord) (bool, error)
}

// SignalForwarder is an explicit hook for wrapper signal forwarding. Keeping
// it injected lets the CLI own signal subscriptions without coupling process
// execution to global os/signal state.
type SignalForwarder interface {
	Forward(context.Context, state.TrackedProcessRecord, os.Signal) error
}

// RunOptions controls one managed command.
type RunOptions struct {
	Dir                string
	Env                []string
	Mode               ProcessMode
	ForegroundTerminal bool
	// SuperviseCancellation keeps the spawned child context alive while the
	// caller context is observed separately. On cancellation, Runner uses the
	// native cleaner to terminate the full process boundary, waits for the
	// leader, and verifies detached descendants have drained before returning.
	// Callers must retain their owning resource when this proof fails.
	SuperviseCancellation bool
	Stdin                 io.Reader
	Stdout                io.Writer
	Stderr                io.Writer
	CaptureLimit          int
	Register              RegisterFunc
	// HandoffComplete is called after registration succeeds, or after failed
	// registration cleanup has settled. Managed callers use it to release the
	// workspace handoff lock before Runner waits for a long-lived child.
	HandoffComplete func()
}

// RunResult contains the normalized child result and bounded diagnostic tails.
type RunResult struct {
	PID int
	// Started is true once a spawner returned a non-nil child. It distinguishes
	// an ordinary pre-spawn failure from an unsafe post-spawn failure where the
	// PID or identity could not be established.
	Started  bool
	ExitCode int
	Signal   os.Signal
	Stdout   string
	Stderr   string
	Record   state.TrackedProcessRecord
}
