// Package dashboard serves the local `ruk ui` web dashboard.
//
// The package owns only the HTTP boundary: loopback-only session checks, the
// embedded page, and a small JSON API. Workspace data and every mutation come
// from a Source, which the CLI implements by running the same commands a user
// would type, so lifecycle rules stay in the modules that own them.
package dashboard

import (
	"context"
	"encoding/json"
)

// Snapshot is the read model the page renders on every poll.
type Snapshot struct {
	GeneratedAt  string       `json:"generatedAt"`
	Host         string       `json:"host"`
	Version      string       `json:"version"`
	Repositories []Repository `json:"repositories"`
}

// Repository groups the Ruk workspaces of one Git repository. Error is set
// when the repository could not be read; its workspaces are then empty.
type Repository struct {
	Name       string      `json:"name"`
	Root       string      `json:"root"`
	CommonDir  string      `json:"commonDir"`
	Error      *string     `json:"error"`
	Workspaces []Workspace `json:"workspaces"`
}

// Workspace is one Ruk-managed or Ruk-created worktree.
type Workspace struct {
	Path           string           `json:"path"`
	Branch         string           `json:"branch"`
	Head           string           `json:"head"`
	Lifecycle      *string          `json:"lifecycle"`
	Managed        bool             `json:"managed"`
	Source         *string          `json:"source"`
	Prepared       bool             `json:"prepared"`
	Mode           *string          `json:"mode"`
	AssignmentID   *string          `json:"assignmentId"`
	Owner          *string          `json:"owner"`
	AssignedAt     *string          `json:"assignedAt"`
	ExpiresAt      *string          `json:"expiresAt"`
	LastActivityAt *string          `json:"lastActivityAt"`
	LeaseMinutes   *float64         `json:"leaseMinutes"`
	AutoRenewing   bool             `json:"autoRenewing"`
	Ports          map[string]int64 `json:"ports"`
	Processes      []Process        `json:"processes"`
	Failure        *string          `json:"failure"`
}

// Process is one command Ruk tracks inside a workspace.
type Process struct {
	PID       int64    `json:"pid"`
	Command   []string `json:"command"`
	StartedAt string   `json:"startedAt"`
}

// ActionKind names one mutation or on-demand query the page can request.
type ActionKind string

const (
	ActionRenew     ActionKind = "renew"
	ActionRelease   ActionKind = "release"
	ActionRemove    ActionKind = "remove"
	ActionGCPreview ActionKind = "gc-preview"
	ActionGCApply   ActionKind = "gc-apply"
	ActionDisk      ActionKind = "disk"
)

// Action is one validated request from the page. Repository is the root path
// reported by the snapshot; the Source re-resolves it before acting.
type Action struct {
	Kind         ActionKind `json:"kind"`
	Repository   string     `json:"repository"`
	AssignmentID string     `json:"assignmentId,omitempty"`
	Path         string     `json:"path,omitempty"`
	Force        bool       `json:"force,omitempty"`
	ForceExpired bool       `json:"forceExpired,omitempty"`
	// TTLMinutes renews for this many minutes. The page sends the
	// assignment's original lease length; zero uses the CLI default.
	TTLMinutes int `json:"ttlMinutes,omitempty"`
}

// ActionResult carries the command's JSON output, or its human message for
// commands without a JSON mode.
type ActionResult struct {
	Result  json.RawMessage `json:"result,omitempty"`
	Message string          `json:"message,omitempty"`
}

// ActionError is a command failure the page should show to the user. It
// mirrors the CLI's JSON error record.
type ActionError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Recovery  string `json:"recovery,omitempty"`
}

func (err *ActionError) Error() string { return err.Message }

// Source supplies workspace data and performs actions.
type Source interface {
	Snapshot(context.Context) (Snapshot, error)
	Act(context.Context, Action) (ActionResult, error)
}
