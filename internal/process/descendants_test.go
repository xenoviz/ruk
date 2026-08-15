package process_test

import (
	"context"
	"os"
	"testing"

	processpkg "github.com/xenoviz/ruk/internal/process"
)

func TestNativeTableContainsCurrentProcess(t *testing.T) {
	t.Parallel()

	entries, err := (processpkg.NativeTable{}).Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot returned an error: %v", err)
	}
	for _, entry := range entries {
		if entry.PID == os.Getpid() {
			return
		}
	}
	t.Fatalf("current process %d was absent from native snapshot", os.Getpid())
}

type staticProcessTable []processpkg.Entry

func (table staticProcessTable) Snapshot(context.Context) ([]processpkg.Entry, error) {
	return append([]processpkg.Entry(nil), table...), nil
}

func TestDescendantInspectorFindsTransitiveChild(t *testing.T) {
	t.Parallel()

	inspector := processpkg.DescendantInspector{Table: staticProcessTable{
		{PID: 10, ParentPID: 1},
		{PID: 11, ParentPID: 10},
		{PID: 12, ParentPID: 11},
		{PID: 20, ParentPID: 1},
	}}
	exists, err := inspector.Exists(context.Background(), 10)
	if err != nil {
		t.Fatalf("Exists returned an error: %v", err)
	}
	if !exists {
		t.Fatal("transitive descendant was reported absent")
	}
}

func TestDescendantInspectorFindsLeaderlessProcessGroup(t *testing.T) {
	t.Parallel()

	inspector := processpkg.DescendantInspector{Table: staticProcessTable{
		{PID: 21, ParentPID: 1, GroupID: 10},
		{PID: 30, ParentPID: 1, GroupID: 30},
	}}
	exists, err := inspector.Exists(context.Background(), 10)
	if err != nil {
		t.Fatalf("Exists returned an error: %v", err)
	}
	if !exists {
		t.Fatal("leaderless process-group member was reported absent")
	}
}
