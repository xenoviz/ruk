package lifecycle

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

// GcCandidateReason explains why a workspace is eligible for collection.
type GcCandidateReason string

const (
	GcAvailable             GcCandidateReason = "available"
	GcFailed                GcCandidateReason = "failed"
	GcAbandonedPreparation  GcCandidateReason = "abandoned-preparation"
	GcAbandonedAcquisition  GcCandidateReason = "abandoned-acquisition"
	GcInterruptedCollection GcCandidateReason = "interrupted-collection"
	GcExpiredAssignment     GcCandidateReason = "expired-assignment"
)

// GcCandidate is an immutable snapshot of a workspace eligible for GC.
//
// The expected fields are copied out explicitly because collection is normally
// performed after releasing the state lock. They are fences, not hints: a
// collector must revalidate these values before mutating or deleting anything.
type GcCandidate struct {
	Workspace            state.WorkspaceRecord
	Reason               GcCandidateReason
	RequiresForce        bool
	ExpectedUpdatedAt    string
	ExpectedOperationID  *string
	ExpectedAssignmentID *string
	ExpectedRenewedAt    *string
	ExpectedExpiresAt    *string
}

// GCCandidate and GCReason are aliases for callers that use Go's initialism
// spelling in exported names.
type GCCandidate = GcCandidate
type GCReason = GcCandidateReason

const (
	GCReasonAvailable             = GcAvailable
	GCReasonFailed                = GcFailed
	GCReasonAbandonedPreparation  = GcAbandonedPreparation
	GCReasonAbandonedAcquisition  = GcAbandonedAcquisition
	GCReasonInterruptedCollection = GcInterruptedCollection
	GCReasonExpiredAssignment     = GcExpiredAssignment
)

// IdentifyGCCandidates returns a read-only, deterministic GC plan from a
// state snapshot. Safe stale records are always considered. Abandoned
// in-flight records are considered only when includeAbandoned is true,
// matching the explicit recovery policy. Expired assignments are reported but
// never become safe candidates.
func IdentifyGCCandidates(current *state.State, olderThan, now time.Time, includeAbandoned bool) ([]GcCandidate, error) {
	if current == nil {
		return nil, errors.New("lifecycle: nil state")
	}
	if current.Version != state.CurrentVersion {
		return nil, fmt.Errorf("state version %d is not supported; expected %d", current.Version, state.CurrentVersion)
	}
	if current.Workspaces == nil {
		return nil, errors.New("lifecycle: state workspaces are missing")
	}
	cutoff := olderThan.UTC().Truncate(time.Millisecond)
	now = now.UTC().Truncate(time.Millisecond)
	if cutoff.IsZero() {
		return nil, errors.New("olderThan must be a valid timestamp")
	}
	if now.IsZero() {
		return nil, errors.New("now must be a valid timestamp")
	}

	result := make([]GcCandidate, 0)
	for _, workspace := range current.Workspaces {
		candidate, ok, err := gcCandidate(workspace, cutoff, now, includeAbandoned)
		if err != nil {
			return nil, err
		}
		if ok {
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Workspace.Path < result[right].Workspace.Path
	})
	return result, nil
}

// IdentifyGcCandidates is the spelling retained for callers that mirror the
// TypeScript lifecycle API.
func IdentifyGcCandidates(current *state.State, olderThan, now time.Time, includeAbandoned bool) ([]GcCandidate, error) {
	return IdentifyGCCandidates(current, olderThan, now, includeAbandoned)
}

func gcCandidate(workspace state.WorkspaceRecord, cutoff, now time.Time, includeAbandoned bool) (GcCandidate, bool, error) {
	workspace = cloneWorkspace(workspace)
	updatedAt, err := parseTimestamp(workspace.UpdatedAt)
	if err != nil {
		return GcCandidate{}, false, fmt.Errorf("workspace %s: %w", workspace.Path, err)
	}
	if workspace.OperationID != nil && includeAbandoned {
		switch workspace.Lifecycle {
		case state.LifecyclePreparing:
			if !updatedAt.After(cutoff) {
				return makeGCCandidate(workspace, GcAbandonedPreparation, false), true, nil
			}
		case state.LifecycleAssigned:
			if !updatedAt.After(cutoff) {
				return makeGCCandidate(workspace, GcAbandonedAcquisition, false), true, nil
			}
		case state.LifecycleAvailable, state.LifecycleFailed:
			// An operation fence on an available or failed workspace means a
			// collection was already started. It must remain retryable even
			// when BeginWorkspaceCollection refreshed UpdatedAt immediately
			// before an external Git/tree-state step failed. The operation ID,
			// update fence, and per-workspace lock still revalidate the retry;
			// the age cutoff only determines whether abandoned preparation or
			// acquisition work should be recovered.
			return makeGCCandidate(workspace, GcInterruptedCollection, false), true, nil
		}
	}

	if workspace.OperationID == nil && workspace.Lifecycle == state.LifecycleAvailable {
		if workspace.AvailableAt == nil {
			return GcCandidate{}, false, fmt.Errorf("workspace %s: available workspace has no availableAt timestamp", workspace.Path)
		}
		availableAt, parseErr := parseTimestamp(*workspace.AvailableAt)
		if parseErr != nil {
			return GcCandidate{}, false, fmt.Errorf("workspace %s: %w", workspace.Path, parseErr)
		}
		if !availableAt.After(cutoff) {
			return makeGCCandidate(workspace, GcAvailable, false), true, nil
		}
	}
	if workspace.OperationID == nil && workspace.Lifecycle == state.LifecycleFailed && !updatedAt.After(cutoff) {
		return makeGCCandidate(workspace, GcFailed, false), true, nil
	}

	if workspace.Assignment != nil {
		expiresAt, parseErr := parseTimestamp(workspace.Assignment.ExpiresAt)
		if parseErr != nil {
			return GcCandidate{}, false, fmt.Errorf("workspace %s: %w", workspace.Path, parseErr)
		}
		activeKeeper, keeperErr := assignmentHasActiveKeeper(workspace.Assignment, now)
		if keeperErr != nil {
			return GcCandidate{}, false, fmt.Errorf("workspace %s: %w", workspace.Path, keeperErr)
		}
		if !expiresAt.After(now) && !activeKeeper {
			return makeGCCandidate(workspace, GcExpiredAssignment, true), true, nil
		}
	}
	return GcCandidate{}, false, nil
}

func assignmentHasActiveKeeper(assignment *state.AssignmentRecord, now time.Time) (bool, error) {
	if assignment == nil {
		return false, nil
	}
	for _, keeper := range assignment.LeaseKeepers {
		validUntil, err := parseTimestamp(keeper.ValidUntil)
		if err != nil {
			return false, err
		}
		if validUntil.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func makeGCCandidate(workspace state.WorkspaceRecord, reason GcCandidateReason, requiresForce bool) GcCandidate {
	candidate := GcCandidate{
		Workspace:         workspace,
		Reason:            reason,
		RequiresForce:     requiresForce,
		ExpectedUpdatedAt: workspace.UpdatedAt,
	}
	if workspace.OperationID != nil {
		value := *workspace.OperationID
		candidate.ExpectedOperationID = &value
	}
	if workspace.Assignment != nil {
		assignmentID := workspace.Assignment.ID
		renewedAt := workspace.Assignment.RenewedAt
		expiresAt := workspace.Assignment.ExpiresAt
		candidate.ExpectedAssignmentID = &assignmentID
		candidate.ExpectedRenewedAt = &renewedAt
		candidate.ExpectedExpiresAt = &expiresAt
	}
	return candidate
}

func cloneWorkspace(workspace state.WorkspaceRecord) state.WorkspaceRecord {
	if workspace.OperationID != nil {
		value := *workspace.OperationID
		workspace.OperationID = &value
	}
	if workspace.AvailableAt != nil {
		value := *workspace.AvailableAt
		workspace.AvailableAt = &value
	}
	if workspace.Failure != nil {
		value := *workspace.Failure
		workspace.Failure = &value
	}
	if workspace.Assignment != nil {
		assignment := *workspace.Assignment
		assignment.LeaseKeepers = append([]state.LeaseKeeperRecord(nil), assignment.LeaseKeepers...)
		if assignment.Ports != nil {
			assignment.Ports = make(map[string]int64, len(assignment.Ports))
			for key, value := range workspace.Assignment.Ports {
				assignment.Ports[key] = value
			}
		}
		workspace.Assignment = &assignment
	}
	if workspace.Processes != nil {
		processes := make([]state.TrackedProcessRecord, len(workspace.Processes))
		for index, process := range workspace.Processes {
			processes[index] = cloneProcessRecord(process)
		}
		workspace.Processes = processes
	}
	return workspace
}
