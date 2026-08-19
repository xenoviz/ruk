package cli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
	"github.com/xenoviz/ruk/internal/statistics"
)

func TestBuildListResponseTable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	linked := filepath.Join(filepath.Dir(root), "linked")
	rootKey, err := state.TreeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	linkedKey, err := state.TreeKey(linked)
	if err != nil {
		t.Fatal(err)
	}
	expires := "2026-08-16T12:00:00.000Z"
	activity := "2026-08-16T11:00:00.000Z"
	validUntil := "2026-08-16T13:00:00.000Z"
	assignment := &state.AssignmentRecord{
		ID: "assignment-1", ExpiresAt: expires, LastActivityAt: activity,
		LeaseKeepers: []state.LeaseKeeperRecord{{ValidUntil: validUntil}},
	}
	prepared := state.TreeRecord{Path: root, Fingerprint: strings.Repeat("a", 64), Mode: "managed-install"}
	tests := []struct {
		name            string
		snapshot        state.State
		worktrees       []git.WorktreeRecord
		wantCount       int
		wantStatus      string
		wantManaged     bool
		wantAssigned    bool
		wantAutoRenew   bool
		wantActiveCount int64
	}{
		{name: "empty", snapshot: state.State{}, wantCount: 0},
		{
			name:      "populated prepared",
			snapshot:  state.State{Trees: map[string]state.TreeRecord{rootKey: prepared}},
			worktrees: []git.WorktreeRecord{{Path: root, Branch: "main", Head: "head-1"}},
			wantCount: 1, wantStatus: "prepared", wantActiveCount: 0,
		},
		{
			name: "assigned",
			snapshot: state.State{
				Trees:      map[string]state.TreeRecord{rootKey: prepared},
				Workspaces: map[string]state.WorkspaceRecord{rootKey: {Path: root, Managed: true, Lifecycle: state.LifecycleAssigned, Assignment: assignment}},
			},
			worktrees: []git.WorktreeRecord{{Path: root, Branch: "agent/api", Head: "head-2"}},
			wantCount: 1, wantStatus: "prepared", wantManaged: true, wantAssigned: true, wantAutoRenew: true, wantActiveCount: 1,
		},
		{
			name: "failed",
			snapshot: state.State{
				Workspaces: map[string]state.WorkspaceRecord{linkedKey: {Path: linked, Managed: true, Lifecycle: state.LifecycleFailed}},
			},
			worktrees: []git.WorktreeRecord{{Path: linked, Branch: "(detached)", Head: "head-3"}},
			wantCount: 1, wantStatus: "not-prepared", wantManaged: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildListResponse(ListQueryInput{
				Repository: git.Repository{Root: root, PrimaryRoot: root, PrimaryCheckout: true},
				Snapshot:   test.snapshot, Worktrees: test.worktrees,
				ObservedAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != test.wantCount {
				t.Fatalf("record count = %d, want %d", len(got), test.wantCount)
			}
			if test.wantCount == 0 {
				return
			}
			record := got[0]
			if record.Status != test.wantStatus || record.Managed != test.wantManaged || (record.AssignmentID != nil) != test.wantAssigned || record.AutoRenewing != test.wantAutoRenew || record.ActiveAssignments != test.wantActiveCount {
				t.Fatalf("record = %#v", record)
			}
		})
	}
}

func TestBuildStatusResponseReasons(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	key, err := state.TreeKey(root)
	if err != nil {
		t.Fatal(err)
	}
	tree := state.TreeRecord{Path: root, Fingerprint: "prepared", Mode: "managed-install"}
	tests := []struct {
		name         string
		snapshot     state.State
		current      string
		present      bool
		valid        bool
		wantStatus   string
		wantReason   string
		wantRecovery bool
	}{
		{name: "not prepared", snapshot: state.State{}, current: "current", wantStatus: "sync-required", wantReason: "not-prepared", wantRecovery: true},
		{name: "dependencies missing", snapshot: state.State{Trees: map[string]state.TreeRecord{key: tree}}, current: "prepared", present: false, valid: true, wantStatus: "sync-required", wantReason: "dependencies-missing", wantRecovery: true},
		{name: "fingerprint changed", snapshot: state.State{Trees: map[string]state.TreeRecord{key: tree}}, current: "current", present: true, valid: true, wantStatus: "sync-required", wantReason: "fingerprint-changed", wantRecovery: true},
		{name: "projection changed", snapshot: state.State{Trees: map[string]state.TreeRecord{key: tree}}, current: "prepared", present: true, valid: false, wantStatus: "sync-required", wantReason: "projection-changed", wantRecovery: true},
		{name: "ready", snapshot: state.State{Trees: map[string]state.TreeRecord{key: tree}}, current: "prepared", present: true, valid: true, wantStatus: "ready", wantReason: "", wantRecovery: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildStatusResponse(StatusQueryInput{
				Repository: git.Repository{Root: root}, Snapshot: test.snapshot,
				CurrentFingerprint: test.current, NodeModulesPresent: test.present, ProjectionsValid: test.valid,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.wantStatus || valueOr(got.Reason, "") != test.wantReason || (got.Recovery != nil) != test.wantRecovery {
				t.Fatalf("status = %#v", got)
			}
		})
	}
}

func TestQueryJSONFieldNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	list, err := FormatQueryJSON([]ListRecord{{Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	var listValue []map[string]any
	if err := json.Unmarshal([]byte(list), &listValue); err != nil {
		t.Fatal(err)
	}
	wantListFields := []string{"path", "branch", "head", "fingerprint", "mode", "status", "lifecycle", "assignmentId", "expiresAt", "lastActivityAt", "autoRenewing", "primaryCheckout", "managed", "activeAssignments"}
	for _, field := range wantListFields {
		if _, exists := listValue[0][field]; !exists {
			t.Errorf("list JSON missing %q", field)
		}
	}

	status, err := FormatQueryJSON(StatusRecord{Path: root, Fingerprint: "current"})
	if err != nil {
		t.Fatal(err)
	}
	var statusValue map[string]any
	if err := json.Unmarshal([]byte(status), &statusValue); err != nil {
		t.Fatal(err)
	}
	wantStatusFields := []string{"path", "fingerprint", "preparedFingerprint", "mode", "nodeModulesPresent", "status", "reason", "recovery", "lifecycle", "assignmentId", "expiresAt", "lastActivityAt", "autoRenewing", "primaryCheckout", "managed", "activeAssignments"}
	for _, field := range wantStatusFields {
		if _, exists := statusValue[field]; !exists {
			t.Errorf("status JSON missing %q", field)
		}
	}

	stats := BuildStatsResponse(state.State{}, &statistics.DiskStatistics{EstimatedBytesAvoided: 5})
	encoded, err := FormatQueryJSON(stats)
	if err != nil {
		t.Fatal(err)
	}
	var statsValue map[string]any
	if err := json.Unmarshal([]byte(encoded), &statsValue); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"acquisitions", "workspaceReuses", "preparations", "preparationSkips", "preparationFailures", "totalPreparationMs", "lastPreparationMs", "averagePreparationMs", "reuseRate", "preparationHitRate", "activeAssignments", "availableWorkspaces", "failedWorkspaces", "reservedPorts", "disk"} {
		if _, exists := statsValue[field]; !exists {
			t.Errorf("stats JSON missing %q", field)
		}
	}
}

func TestFormatQueryHumanContracts(t *testing.T) {
	prepared := "abcdef0123456789"
	mode := "managed-install"
	record := StatusRecord{Path: "/repo", Fingerprint: "current", PreparedFingerprint: &prepared, Mode: &mode, NodeModulesPresent: true, Status: "ready"}
	if got := FormatStatusHuman(record, false); !strings.Contains(got, "node_modules: present\n") || strings.Contains(got, "Next:") {
		t.Fatalf("status human output = %q", got)
	}
	stats := BuildStatsResponse(state.State{Metrics: state.UsageMetrics{Acquisitions: 2, Preparations: 1, TotalPreparationMS: 3}}, nil)
	wantStats := "Acquisitions:       2\nWorkspace reuses:   0\nPreparations:       1\nPreparation skips:  0\nPreparation failures: 0\nAverage prepare ms: 3\n"
	if got := FormatStatsHuman(stats); got != wantStats {
		t.Fatalf("stats human output = %q, want %q", got, wantStats)
	}
	if got := FormatListHuman([]ListRecord{{Branch: "main", Fingerprint: &prepared, Mode: &mode, Path: "/repo"}}); !strings.Contains(got, "abcdef012345") {
		t.Fatalf("list human output = %q", got)
	}
	if !reflect.DeepEqual(BuildStatsResponse(state.State{}, nil).Disk, (*statistics.DiskStatistics)(nil)) {
		t.Fatal("stats without --disk should omit disk")
	}
}
