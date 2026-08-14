// Package state owns Ruk's durable repository state and compatibility migrations.
package state

// CurrentVersion is the canonical state schema written by the Go runtime.
const CurrentVersion = 4

// TreeRecord describes dependency preparation for one Git worktree.
type TreeRecord struct {
	Path                  string   `json:"path"`
	Fingerprint           string   `json:"fingerprint"`
	ProjectionFingerprint string   `json:"projectionFingerprint,omitempty"`
	Mode                  string   `json:"mode"`
	Projections           []string `json:"projections"`
	Branch                string   `json:"branch"`
	UpdatedAt             string   `json:"updatedAt"`
}

// WorkspaceLifecycle is one durable workspace state.
type WorkspaceLifecycle string

const (
	LifecycleAvailable WorkspaceLifecycle = "available"
	LifecyclePreparing WorkspaceLifecycle = "preparing"
	LifecycleAssigned  WorkspaceLifecycle = "assigned"
	LifecycleReturning WorkspaceLifecycle = "returning"
	LifecycleFailed    WorkspaceLifecycle = "failed"
)

// LeaseKeeperRecord fences an activity heartbeat that keeps an assignment alive.
type LeaseKeeperRecord struct {
	ID          string `json:"id"`
	HeartbeatAt string `json:"heartbeatAt"`
	ValidUntil  string `json:"validUntil"`
}

// AssignmentRecord is the immutable ownership fence plus its renewable lease.
type AssignmentRecord struct {
	ID                   string              `json:"id"`
	Owner                string              `json:"owner"`
	Hostname             string              `json:"hostname"`
	AssignedAt           string              `json:"assignedAt"`
	RenewedAt            string              `json:"renewedAt"`
	ExpiresAt            string              `json:"expiresAt"`
	LeaseDurationMinutes float64             `json:"leaseDurationMinutes"`
	LastActivityAt       string              `json:"lastActivityAt"`
	LeaseKeepers         []LeaseKeeperRecord `json:"leaseKeepers"`
	Ports                map[string]int64    `json:"ports"`
}

// UsageMetrics contains bounded counters used by the stats command.
type UsageMetrics struct {
	Acquisitions        int64  `json:"acquisitions"`
	WorkspaceReuses     int64  `json:"workspaceReuses"`
	Preparations        int64  `json:"preparations"`
	PreparationSkips    int64  `json:"preparationSkips"`
	PreparationFailures int64  `json:"preparationFailures"`
	TotalPreparationMS  int64  `json:"totalPreparationMs"`
	LastPreparationMS   *int64 `json:"lastPreparationMs"`
}

// EmptyMetrics returns the canonical zero metrics record.
func EmptyMetrics() UsageMetrics {
	return UsageMetrics{}
}

// TrackedProcessRecord identifies one Ruk-owned process without trusting a reusable PID alone.
type TrackedProcessRecord struct {
	PID              int64    `json:"pid"`
	GroupID          *int64   `json:"groupId,omitempty"`
	SessionID        *int64   `json:"sessionId,omitempty"`
	SessionStartedAt *string  `json:"sessionStartedAt,omitempty"`
	TerminalID       *string  `json:"terminalId,omitempty"`
	Command          []string `json:"command,omitempty"`
	StartedAt        string   `json:"startedAt"`
}

// WorkspaceRecord is one managed pooled worktree and its current lifecycle state.
type WorkspaceRecord struct {
	Path        string                 `json:"path"`
	Managed     bool                   `json:"managed"`
	Branch      string                 `json:"branch"`
	Lifecycle   WorkspaceLifecycle     `json:"lifecycle"`
	OperationID *string                `json:"operationId"`
	Assignment  *AssignmentRecord      `json:"assignment"`
	Processes   []TrackedProcessRecord `json:"processes"`
	CreatedAt   string                 `json:"createdAt"`
	UpdatedAt   string                 `json:"updatedAt"`
	AvailableAt *string                `json:"availableAt"`
	Failure     *string                `json:"failure"`
}

// State is the canonical in-memory version-four state.
type State struct {
	Version    int                        `json:"version"`
	Trees      map[string]TreeRecord      `json:"trees"`
	Workspaces map[string]WorkspaceRecord `json:"workspaces"`
	Metrics    UsageMetrics               `json:"metrics"`
}
