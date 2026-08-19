// Package statistics derives bounded usage counters and on-demand disk
// measurements from a durable Ruk state snapshot.
package statistics

import (
	"math"

	"github.com/xenoviz/ruk/internal/state"
)

// UsageStatistics is the JSON-facing aggregate returned by the stats command.
// The metric counters themselves are copied from state so callers cannot
// accidentally mutate the snapshot while formatting a response.
type UsageStatistics struct {
	Acquisitions        int64  `json:"acquisitions"`
	WorkspaceReuses     int64  `json:"workspaceReuses"`
	Preparations        int64  `json:"preparations"`
	PreparationSkips    int64  `json:"preparationSkips"`
	PreparationFailures int64  `json:"preparationFailures"`
	TotalPreparationMS  int64  `json:"totalPreparationMs"`
	LastPreparationMS   *int64 `json:"lastPreparationMs"`

	AveragePreparationMS int64   `json:"averagePreparationMs"`
	ReuseRate            float64 `json:"reuseRate"`
	PreparationHitRate   float64 `json:"preparationHitRate"`
	ActiveAssignments    int64   `json:"activeAssignments"`
	AvailableWorkspaces  int64   `json:"availableWorkspaces"`
	FailedWorkspaces     int64   `json:"failedWorkspaces"`
	ReservedPorts        int64   `json:"reservedPorts"`
}

// Usage computes aggregate counters without scanning the filesystem. An
// available workspace with an operation fence is still in handoff and is not
// counted as capacity. Returning workspaces retain their assignment and are
// intentionally included in ActiveAssignments.
func Usage(snapshot state.State) UsageStatistics {
	metrics := snapshot.Metrics
	attempts := metrics.Preparations + metrics.PreparationSkips + metrics.PreparationFailures
	average := int64(0)
	if metrics.Preparations != 0 {
		average = int64(math.Round(float64(metrics.TotalPreparationMS) / float64(metrics.Preparations)))
	}
	reuseRate := float64(0)
	if metrics.Acquisitions != 0 {
		reuseRate = float64(metrics.WorkspaceReuses) / float64(metrics.Acquisitions)
	}
	hitRate := float64(0)
	if attempts != 0 {
		hitRate = float64(metrics.PreparationSkips) / float64(attempts)
	}

	result := UsageStatistics{
		Acquisitions:         metrics.Acquisitions,
		WorkspaceReuses:      metrics.WorkspaceReuses,
		Preparations:         metrics.Preparations,
		PreparationSkips:     metrics.PreparationSkips,
		PreparationFailures:  metrics.PreparationFailures,
		TotalPreparationMS:   metrics.TotalPreparationMS,
		LastPreparationMS:    metrics.LastPreparationMS,
		AveragePreparationMS: average,
		ReuseRate:            reuseRate,
		PreparationHitRate:   hitRate,
		ActiveAssignments:    0,
		AvailableWorkspaces:  0,
		FailedWorkspaces:     0,
		ReservedPorts:        0,
	}
	for _, workspace := range snapshot.Workspaces {
		if workspace.Assignment != nil {
			result.ActiveAssignments++
			result.ReservedPorts += int64(len(workspace.Assignment.Ports))
		}
		if workspace.Lifecycle == state.LifecycleAvailable && workspace.OperationID == nil {
			result.AvailableWorkspaces++
		}
		if workspace.Lifecycle == state.LifecycleFailed {
			result.FailedWorkspaces++
		}
	}
	return result
}

// UsageStatisticsFor is a descriptive alias for callers migrating from the
// TypeScript usageStatistics function.
func UsageStatisticsFor(snapshot state.State) UsageStatistics { return Usage(snapshot) }

// ComputeUsageStatistics is an explicit alias for Usage.
func ComputeUsageStatistics(snapshot state.State) UsageStatistics { return Usage(snapshot) }
