package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
	"github.com/xenoviz/ruk/internal/statistics"
)

// ListRecord is the JSON-facing record returned by list. Pointer fields are
// intentional: the TypeScript CLI emits null for facts that are not present.
type ListRecord struct {
	Path              string                    `json:"path"`
	Branch            string                    `json:"branch"`
	Head              string                    `json:"head"`
	Fingerprint       *string                   `json:"fingerprint"`
	Mode              *string                   `json:"mode"`
	Status            string                    `json:"status"`
	Lifecycle         *state.WorkspaceLifecycle `json:"lifecycle"`
	AssignmentID      *string                   `json:"assignmentId"`
	ExpiresAt         *string                   `json:"expiresAt"`
	LastActivityAt    *string                   `json:"lastActivityAt"`
	AutoRenewing      bool                      `json:"autoRenewing"`
	PrimaryCheckout   bool                      `json:"primaryCheckout"`
	Managed           bool                      `json:"managed"`
	ActiveAssignments int64                     `json:"activeAssignments"`
}

// ListResponse is the complete JSON result for list.
type ListResponse = []ListRecord

// ListQueryInput contains only facts needed to build list output. Worktree
// discovery and state loading happen outside the pure response builder.
type ListQueryInput struct {
	Repository        git.Repository
	Snapshot          state.State
	Worktrees         []git.WorktreeRecord
	ObservedAt        time.Time
	ActiveAssignments int64
}

// BuildListResponse derives the public list records from a state snapshot and
// Git inventory. It performs no I/O and does not mutate either input.
func BuildListResponse(input ListQueryInput) ([]ListRecord, error) {
	activeAssignments := input.ActiveAssignments
	if activeAssignments == 0 {
		activeAssignments = statistics.Usage(input.Snapshot).ActiveAssignments
	}
	now := input.ObservedAt
	if now.IsZero() {
		now = time.Now()
	}
	result := make([]ListRecord, 0, len(input.Worktrees))
	for _, worktree := range input.Worktrees {
		key, err := state.TreeKey(worktree.Path)
		if err != nil {
			return nil, fmt.Errorf("derive state key for %s: %w", worktree.Path, err)
		}
		tree, prepared := input.Snapshot.Trees[key]
		workspace, managed := input.Snapshot.Workspaces[key]
		record := ListRecord{
			Path:              worktree.Path,
			Branch:            worktree.Branch,
			Head:              worktree.Head,
			Status:            "not-prepared",
			AutoRenewing:      false,
			PrimaryCheckout:   sameQueryPath(worktree.Path, input.Repository.PrimaryRoot),
			Managed:           managed,
			ActiveAssignments: activeAssignments,
		}
		if prepared {
			fingerprint, mode := tree.Fingerprint, tree.Mode
			record.Fingerprint = &fingerprint
			record.Mode = &mode
			record.Status = "prepared"
		}
		if managed {
			lifecycle := workspace.Lifecycle
			record.Lifecycle = &lifecycle
			if workspace.Assignment != nil {
				assignment := workspace.Assignment
				record.AssignmentID = stringPointer(assignment.ID)
				record.ExpiresAt = stringPointer(assignment.ExpiresAt)
				record.LastActivityAt = stringPointer(assignment.LastActivityAt)
				record.AutoRenewing = AssignmentIsAutoRenewing(*assignment, now)
			}
		}
		result = append(result, record)
	}
	return result, nil
}

// AssignmentIsAutoRenewing reports whether a current fenced lease keeper is
// visible at observedAt. Invalid timestamps fail closed, as state validation
// and the TypeScript lifecycle predicate do.
func AssignmentIsAutoRenewing(assignment state.AssignmentRecord, observedAt time.Time) bool {
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	for _, keeper := range assignment.LeaseKeepers {
		validUntil, err := time.Parse(time.RFC3339Nano, keeper.ValidUntil)
		if err == nil && validUntil.After(observedAt) {
			return true
		}
	}
	return false
}

// StatusRecord is the JSON-facing response for status.
type StatusRecord struct {
	Path                string                    `json:"path"`
	Fingerprint         string                    `json:"fingerprint"`
	PreparedFingerprint *string                   `json:"preparedFingerprint"`
	Mode                *string                   `json:"mode"`
	NodeModulesPresent  bool                      `json:"nodeModulesPresent"`
	Status              string                    `json:"status"`
	Reason              *string                   `json:"reason"`
	Recovery            *string                   `json:"recovery"`
	Lifecycle           *state.WorkspaceLifecycle `json:"lifecycle"`
	AssignmentID        *string                   `json:"assignmentId"`
	ExpiresAt           *string                   `json:"expiresAt"`
	LastActivityAt      *string                   `json:"lastActivityAt"`
	AutoRenewing        bool                      `json:"autoRenewing"`
	PrimaryCheckout     bool                      `json:"primaryCheckout"`
	Managed             bool                      `json:"managed"`
	ActiveAssignments   int64                     `json:"activeAssignments"`
}

// StatusResponse is the complete JSON result for status.
type StatusResponse = StatusRecord

// StatusQueryInput contains all filesystem-derived facts needed for status.
// CurrentFingerprint, NodeModulesPresent, and ProjectionsValid are injected
// by the repository/dependencies layers so this builder remains pure.
type StatusQueryInput struct {
	Repository         git.Repository
	Snapshot           state.State
	CurrentFingerprint string
	NodeModulesPresent bool
	ProjectionsValid   bool
	ObservedAt         time.Time
}

// BuildStatusResponse derives the public status response without performing
// dependency or filesystem operations.
func BuildStatusResponse(input StatusQueryInput) (StatusRecord, error) {
	key, err := state.TreeKey(input.Repository.Root)
	if err != nil {
		return StatusRecord{}, fmt.Errorf("derive state key for %s: %w", input.Repository.Root, err)
	}
	tree, prepared := input.Snapshot.Trees[key]
	workspace, managed := input.Snapshot.Workspaces[key]
	ready := prepared && tree.Fingerprint == input.CurrentFingerprint && input.ProjectionsValid
	record := StatusRecord{
		Path:               input.Repository.Root,
		Fingerprint:        input.CurrentFingerprint,
		NodeModulesPresent: input.NodeModulesPresent,
		Status:             "sync-required",
		PrimaryCheckout:    input.Repository.PrimaryCheckout,
		Managed:            managed,
		ActiveAssignments:  statistics.Usage(input.Snapshot).ActiveAssignments,
	}
	if prepared {
		fingerprint, mode := tree.Fingerprint, tree.Mode
		record.PreparedFingerprint = &fingerprint
		record.Mode = &mode
	}
	if ready {
		record.Status = "ready"
	}
	if !ready {
		reason := "not-prepared"
		switch {
		case prepared && !input.NodeModulesPresent:
			reason = "dependencies-missing"
		case prepared && tree.Fingerprint != input.CurrentFingerprint:
			reason = "fingerprint-changed"
		case prepared:
			reason = "projection-changed"
		}
		record.Reason = stringPointer(reason)
		recovery := "ruk sync"
		record.Recovery = &recovery
	}
	if managed {
		lifecycle := workspace.Lifecycle
		record.Lifecycle = &lifecycle
		if workspace.Assignment != nil {
			assignment := workspace.Assignment
			record.AssignmentID = stringPointer(assignment.ID)
			record.ExpiresAt = stringPointer(assignment.ExpiresAt)
			record.LastActivityAt = stringPointer(assignment.LastActivityAt)
			record.AutoRenewing = AssignmentIsAutoRenewing(*assignment, input.ObservedAt)
		}
	}
	return record, nil
}

// StatsRecord is the JSON-facing response for stats. Disk is absent unless
// --disk was requested, matching the TypeScript object spread behavior.
type StatsRecord struct {
	statistics.UsageStatistics
	Disk *statistics.DiskStatistics `json:"disk,omitempty"`
}

// StatsResponse is the complete JSON result for stats.
type StatsResponse = StatsRecord

// BuildStatsResponse derives aggregate counters through the statistics
// package and optionally attaches a caller-provided on-demand disk result.
func BuildStatsResponse(snapshot state.State, disk *statistics.DiskStatistics) StatsRecord {
	return StatsRecord{UsageStatistics: statistics.Usage(snapshot), Disk: disk}
}

// QueryDependencies is the narrow I/O seam used by the command handlers.
// Production composition supplies the repository/state/dependency functions;
// tests can provide deterministic fakes without creating a repository.
type QueryDependencies struct {
	ListWorktrees       func(context.Context, string) ([]git.WorktreeRecord, error)
	ReadState           func(context.Context, string) (state.State, error)
	CurrentFingerprint  func(context.Context, string) (string, error)
	DependenciesPresent func(context.Context, string, []string) (bool, error)
	ProjectionsValid    func(context.Context, string, state.TreeRecord) (bool, error)
	MeasureDisk         func(context.Context, state.State) (statistics.DiskStatistics, error)
}

// HandleList loads query inputs through injected readers and builds list
// output. It never writes to a stream.
func (dependencies QueryDependencies) HandleList(ctx context.Context, repository git.Repository, observedAt time.Time) ([]ListRecord, error) {
	if dependencies.ListWorktrees == nil || dependencies.ReadState == nil {
		return nil, errors.New("list query dependencies are incomplete")
	}
	snapshot, err := dependencies.ReadState(ctx, repository.CommonDir)
	if err != nil {
		return nil, err
	}
	worktrees, err := dependencies.ListWorktrees(ctx, repository.Root)
	if err != nil {
		return nil, err
	}
	return BuildListResponse(ListQueryInput{Repository: repository, Snapshot: snapshot, Worktrees: worktrees, ObservedAt: observedAt})
}

// HandleStatus loads the dependency facts needed for status through injected
// readers and builds the stable response.
func (dependencies QueryDependencies) HandleStatus(ctx context.Context, repository git.Repository, observedAt time.Time) (StatusRecord, error) {
	if dependencies.ReadState == nil || dependencies.CurrentFingerprint == nil || dependencies.DependenciesPresent == nil || dependencies.ProjectionsValid == nil {
		return StatusRecord{}, errors.New("status query dependencies are incomplete")
	}
	snapshot, err := dependencies.ReadState(ctx, repository.CommonDir)
	if err != nil {
		return StatusRecord{}, err
	}
	fingerprint, err := dependencies.CurrentFingerprint(ctx, repository.Root)
	if err != nil {
		return StatusRecord{}, err
	}
	key, err := state.TreeKey(repository.Root)
	if err != nil {
		return StatusRecord{}, err
	}
	tree, prepared := snapshot.Trees[key]
	projections := []string{"node_modules"}
	if prepared && tree.Projections != nil {
		projections = tree.Projections
	}
	present, err := dependencies.DependenciesPresent(ctx, repository.Root, projections)
	if err != nil {
		return StatusRecord{}, err
	}
	valid := false
	if prepared {
		valid, err = dependencies.ProjectionsValid(ctx, repository.Root, tree)
		if err != nil {
			return StatusRecord{}, err
		}
	}
	return BuildStatusResponse(StatusQueryInput{
		Repository: repository, Snapshot: snapshot, CurrentFingerprint: fingerprint,
		NodeModulesPresent: present, ProjectionsValid: valid, ObservedAt: observedAt,
	})
}

// HandleStats loads state and optionally performs the injected on-demand disk
// scan. The ordinary stats path never invokes MeasureDisk.
func (dependencies QueryDependencies) HandleStats(ctx context.Context, repository git.Repository, disk bool) (StatsRecord, error) {
	if dependencies.ReadState == nil {
		return StatsRecord{}, errors.New("stats query dependencies are incomplete")
	}
	snapshot, err := dependencies.ReadState(ctx, repository.CommonDir)
	if err != nil {
		return StatsRecord{}, err
	}
	if !disk {
		return BuildStatsResponse(snapshot, nil), nil
	}
	if dependencies.MeasureDisk == nil {
		return StatsRecord{}, errors.New("disk stats query dependency is incomplete")
	}
	measurement, err := dependencies.MeasureDisk(ctx, snapshot)
	if err != nil {
		return StatsRecord{}, err
	}
	return BuildStatsResponse(snapshot, &measurement), nil
}

// FormatListHuman preserves the TypeScript list table layout.
func FormatListHuman(records []ListRecord) string {
	var output strings.Builder
	for _, record := range records {
		fingerprint := "not-prepared"
		if record.Fingerprint != nil {
			fingerprint = *record.Fingerprint
		}
		mode := "-"
		if record.Mode != nil {
			mode = *record.Mode
		}
		if len(fingerprint) > 12 {
			fingerprint = fingerprint[:12]
		}
		fmt.Fprintf(&output, "%-28s %-14s %-20s %s\n", record.Branch, fingerprint, mode, record.Path)
	}
	return output.String()
}

// FormatStatusHuman preserves the TypeScript status labels and explain flow.
func FormatStatusHuman(record StatusRecord, explain bool) string {
	var output strings.Builder
	fingerprint := valueOr(record.PreparedFingerprint, "not-prepared")
	mode := valueOr(record.Mode, "-")
	output.WriteString(fmt.Sprintf("Workspace:   %s\n", record.Path))
	output.WriteString(fmt.Sprintf("Fingerprint: %s\n", record.Fingerprint))
	output.WriteString(fmt.Sprintf("Prepared:    %s\n", fingerprint))
	output.WriteString(fmt.Sprintf("Mode:        %s\n", mode))
	if record.NodeModulesPresent {
		output.WriteString("node_modules: present\n")
	} else {
		output.WriteString("node_modules: missing\n")
	}
	output.WriteString(fmt.Sprintf("Status:      %s\n", record.Status))
	if explain && record.Reason != nil {
		output.WriteString(fmt.Sprintf("Reason:      %s\nRecovery:    %s\n", *record.Reason, valueOr(record.Recovery, "")))
	}
	lifecycle := "unmanaged"
	if record.Lifecycle != nil {
		lifecycle = string(*record.Lifecycle)
	}
	output.WriteString(fmt.Sprintf("Lifecycle:   %s\n", lifecycle))
	if record.AssignmentID != nil {
		output.WriteString(fmt.Sprintf("Assignment:  %s (expires %s)\n", *record.AssignmentID, valueOr(record.ExpiresAt, "")))
		activity := "idle"
		if record.AutoRenewing {
			activity = "auto-renewing"
		}
		output.WriteString(fmt.Sprintf("Activity:    %s (%s)\n", valueOr(record.LastActivityAt, ""), activity))
	}
	if record.PrimaryCheckout {
		plural := "assignments"
		if record.ActiveAssignments == 1 {
			plural = "assignment"
		}
		output.WriteString(fmt.Sprintf("Checkout:    primary (%d active %s)\n", record.ActiveAssignments, plural))
	}
	if record.Status != "ready" && !explain {
		output.WriteString("Next:        ruk sync\n")
	}
	return output.String()
}

// FormatStatsHuman preserves the TypeScript stats labels and spacing.
func FormatStatsHuman(record StatsRecord) string {
	var output strings.Builder
	output.WriteString(fmt.Sprintf("Acquisitions:       %d\n", record.Acquisitions))
	output.WriteString(fmt.Sprintf("Workspace reuses:   %d\n", record.WorkspaceReuses))
	output.WriteString(fmt.Sprintf("Preparations:       %d\n", record.Preparations))
	output.WriteString(fmt.Sprintf("Preparation skips:  %d\n", record.PreparationSkips))
	output.WriteString(fmt.Sprintf("Preparation failures: %d\n", record.PreparationFailures))
	output.WriteString(fmt.Sprintf("Average prepare ms: %d\n", record.AveragePreparationMS))
	if record.Disk != nil {
		output.WriteString(fmt.Sprintf("Estimated bytes avoided: %d\n", record.Disk.EstimatedBytesAvoided))
	}
	return output.String()
}

// FormatQueryJSON serializes one query response with the required newline.
func FormatQueryJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded) + "\n", nil
}

// FormatList chooses the machine-readable or human list formatter.
func FormatList(records []ListRecord, jsonMode bool) (string, error) {
	if jsonMode {
		return FormatQueryJSON(records)
	}
	return FormatListHuman(records), nil
}

// FormatStatus chooses the machine-readable or human status formatter.
func FormatStatus(record StatusRecord, jsonMode, explain bool) (string, error) {
	if jsonMode {
		return FormatQueryJSON(record)
	}
	return FormatStatusHuman(record, explain), nil
}

// FormatStats chooses the machine-readable or human stats formatter.
func FormatStats(record StatsRecord, jsonMode bool) (string, error) {
	if jsonMode {
		return FormatQueryJSON(record)
	}
	return FormatStatsHuman(record), nil
}

func stringPointer(value string) *string { return &value }

func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func sameQueryPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
