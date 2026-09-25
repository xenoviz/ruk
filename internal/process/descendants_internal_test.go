package process

import (
	"reflect"
	"testing"
)

func TestDescendantsCreatedAfterParentsSkipsReusedParentPID(t *testing.T) {
	t.Parallel()

	// PID 100 is the tracked leader. PID 200 (an editor) was started by an
	// earlier process that also had PID 100, so its stale parent link names the
	// leader even though it is older. Its own child 201 must be skipped too.
	entries := []Entry{
		{PID: 100, ParentPID: 1},
		{PID: 110, ParentPID: 100},
		{PID: 111, ParentPID: 110},
		{PID: 200, ParentPID: 100},
		{PID: 201, ParentPID: 200},
	}
	identities := map[int]string{
		100: "5000",
		110: "5001",
		111: "5002",
		200: "4000",
		201: "6000",
	}
	pids := []int{110, 200, 111, 201, 100}
	got := descendantsCreatedAfterParents(entries, 100, pids, identities)
	want := []int{110, 111, 100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descendants = %v, want %v (from %v)", got, want, pids)
	}
}

func TestDescendantsCreatedAfterParentsFollowsIncomparableIdentities(t *testing.T) {
	t.Parallel()

	entries := []Entry{{PID: 100, ParentPID: 1}, {PID: 110, ParentPID: 100}}
	got := descendantsCreatedAfterParents(entries, 100, []int{110, 100}, map[int]string{100: "5000", 110: "linux:1:2"})
	if !reflect.DeepEqual(got, []int{110, 100}) {
		t.Fatalf("descendants = %v", got)
	}
}
