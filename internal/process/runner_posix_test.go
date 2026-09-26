//go:build !windows

package process_test

import (
	"context"
	"testing"
	"time"

	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

// exitFirstDescriber waits until the detached child has exited, unreaped,
// before describing it. That reproduces a short-lived command such as a
// runtime version probe finishing before the runner records its identity.
type exitFirstDescriber struct {
	t     *testing.T
	inner processpkg.NativeProcessDescriber
}

func (describer exitFirstDescriber) Describe(ctx context.Context, pid int, mode processpkg.ProcessMode, command []string) (state.TrackedProcessRecord, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		exited, err := (processpkg.NativeTable{}).ExitedGroupLeader(ctx, pid)
		if err != nil {
			describer.t.Errorf("ExitedGroupLeader returned an error: %v", err)
			break
		}
		if exited {
			break
		}
		if time.Now().After(deadline) {
			describer.t.Errorf("child %d did not become an exited group leader", pid)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return describer.inner.Describe(ctx, pid, mode, command)
}

func TestRunnerRecordsDetachedChildThatExitsBeforeIdentityCapture(t *testing.T) {
	t.Parallel()
	runner := processpkg.NewRunner()
	runner.Describer = exitFirstDescriber{t: t, inner: processpkg.NativeProcessDescriber{Probe: processpkg.Inspector{}, Table: processpkg.NativeTable{}}}
	var registered state.TrackedProcessRecord
	result, err := runner.Run(context.Background(), []string{"sh", "-c", "echo done"}, processpkg.RunOptions{
		Mode: processpkg.Detached,
		Register: func(_ context.Context, record state.TrackedProcessRecord) error {
			registered = record
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if result.ExitCode != 0 || result.Stdout != "done\n" {
		t.Fatalf("result = %#v, want exit 0 with captured output", result)
	}
	if processpkg.IsUnverifiedRecord(registered) || registered.GroupID == nil || *registered.GroupID != registered.PID {
		t.Fatalf("registered = %#v, want an exact identity that leads its group", registered)
	}
}

func TestNativeTableExitedGroupLeaderRejectsLiveAndMissingProcesses(t *testing.T) {
	t.Parallel()
	table := processpkg.NativeTable{}
	// The test process is alive, so it is not an exited leader.
	if exited, err := table.ExitedGroupLeader(context.Background(), 1); err != nil || exited {
		t.Fatalf("ExitedGroupLeader(1) = %v, %v; want false, nil", exited, err)
	}
	if exited, err := table.ExitedGroupLeader(context.Background(), 1<<22+12345); err != nil || exited {
		t.Fatalf("ExitedGroupLeader(missing) = %v, %v; want false, nil", exited, err)
	}
}
