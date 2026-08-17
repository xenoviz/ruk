package statistics

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xenoviz/ruk/internal/state"
)

func TestUsageLifecycleAndCapacityCounters(t *testing.T) {
	operation := "operation-fence"
	assignment := &state.AssignmentRecord{Ports: map[string]int64{"api": 4100}}
	tests := []struct {
		name       string
		metrics    state.UsageMetrics
		workspaces map[string]state.WorkspaceRecord
		want       UsageStatistics
	}{
		{
			name: "fenced available capacity is excluded",
			workspaces: map[string]state.WorkspaceRecord{
				"available": {Lifecycle: state.LifecycleAvailable},
				"fenced":    {Lifecycle: state.LifecycleAvailable, OperationID: &operation},
			},
			want: UsageStatistics{AvailableWorkspaces: 1},
		},
		{
			name: "returning assignment remains active",
			workspaces: map[string]state.WorkspaceRecord{
				"returning": {Lifecycle: state.LifecycleReturning, Assignment: assignment},
				"failed":    {Lifecycle: state.LifecycleFailed},
			},
			want: UsageStatistics{ActiveAssignments: 1, FailedWorkspaces: 1, ReservedPorts: 1},
		},
		{
			name: "metrics ratios and average",
			metrics: state.UsageMetrics{
				Acquisitions: 4, WorkspaceReuses: 3, Preparations: 2,
				PreparationSkips: 2, PreparationFailures: 1, TotalPreparationMS: 31,
			},
			want: UsageStatistics{
				Acquisitions: 4, WorkspaceReuses: 3, Preparations: 2,
				PreparationSkips: 2, PreparationFailures: 1, TotalPreparationMS: 31,
				AveragePreparationMS: 16, ReuseRate: .75, PreparationHitRate: 2.0 / 5.0,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Usage(state.State{Metrics: test.metrics, Workspaces: test.workspaces})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Usage() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMeasureDiskStatisticsUsesCurrentProjectionsAndDeduplicatesTargets(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	projection := filepath.Join(workspace, "node_modules")
	target := filepath.Join(root, "store", "package")
	child := filepath.Join(root, "store", "child")
	if err := os.MkdirAll(projection, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "index.js"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "child.js"), []byte("1234567"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(child, filepath.Join(target, "child")); err != nil {
		t.Skip("symlinks are unavailable")
	}
	if err := os.Symlink(target, filepath.Join(projection, "parent")); err != nil {
		t.Skip("symlinks are unavailable")
	}
	if err := os.Symlink(child, filepath.Join(projection, "child")); err != nil {
		t.Skip("symlinks are unavailable")
	}

	key, err := state.TreeKey(workspace)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.State{
		Trees:      map[string]state.TreeRecord{key: {Path: workspace, Projections: []string{"node_modules"}}},
		Workspaces: map[string]state.WorkspaceRecord{key: {Path: workspace}},
	}
	got, err := MeasureDiskStatistics(context.Background(), snapshot, DiskOptions{Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := (DiskStatistics{LinkedTargetBytes: 12}); !reflect.DeepEqual(got, want) {
		t.Fatalf("MeasureDiskStatistics() = %#v, want %#v", got, want)
	}
}

func TestMeasureDiskStatisticsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := MeasureDiskStatistics(ctx, state.State{}, DiskOptions{Concurrency: 1})
	if err != context.Canceled {
		t.Fatalf("MeasureDiskStatistics() error = %v, want %v", err, context.Canceled)
	}
}

func TestMeasureDiskStatisticsCountsSymlinkedProjectionRootsAsLinkedTargets(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "store", "package")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "index.js"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspaceA := filepath.Join(root, "workspace-a")
	workspaceB := filepath.Join(root, "workspace-b")
	if err := os.MkdirAll(workspaceA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspaceB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspaceA, "node_modules")); err != nil {
		t.Skip("symlinks are unavailable")
	}
	if err := os.Symlink(target, filepath.Join(workspaceB, "node_modules")); err != nil {
		t.Skip("symlinks are unavailable")
	}
	keyA, err := state.TreeKey(workspaceA)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := state.TreeKey(workspaceB)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.State{
		Trees: map[string]state.TreeRecord{
			keyA: {Path: workspaceA, Projections: []string{"node_modules"}},
			keyB: {Path: workspaceB, Projections: []string{"node_modules"}},
		},
		Workspaces: map[string]state.WorkspaceRecord{
			keyA: {Path: workspaceA},
			keyB: {Path: workspaceB},
		},
	}
	got, err := MeasureDiskStatistics(context.Background(), snapshot, DiskOptions{Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := DiskStatistics{LinkedTargetBytes: 5, EstimatedBytesAvoided: 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MeasureDiskStatistics() = %#v, want %#v", got, want)
	}
}
