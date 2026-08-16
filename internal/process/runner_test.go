package process_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type runnerChild struct {
	pid    int
	status processpkg.ExitStatus

	waited  bool
	signals []os.Signal
}

func (child *runnerChild) PID() int { return child.pid }
func (child *runnerChild) Wait() processpkg.ExitStatus {
	child.waited = true
	return child.status
}
func (child *runnerChild) Signal(signal os.Signal) error {
	child.signals = append(child.signals, signal)
	return nil
}

type runnerSpawner struct {
	child   *runnerChild
	request processpkg.SpawnRequest
	stdout  string
	stderr  string
}

func (spawner *runnerSpawner) Spawn(_ context.Context, request processpkg.SpawnRequest) (processpkg.Child, error) {
	spawner.request = request
	if spawner.stdout != "" && request.Stdout != nil {
		_, _ = request.Stdout.Write([]byte(spawner.stdout))
	}
	if spawner.stderr != "" && request.Stderr != nil {
		_, _ = request.Stderr.Write([]byte(spawner.stderr))
	}
	return spawner.child, nil
}

type runnerDescriber struct {
	record state.TrackedProcessRecord
	mode   processpkg.ProcessMode
	err    error
}

func (describer runnerDescriber) Describe(_ context.Context, _ int, mode processpkg.ProcessMode, command []string) (state.TrackedProcessRecord, error) {
	describer.mode = mode
	record := describer.record
	record.Command = append([]string(nil), command...)
	return record, describer.err
}

type runnerCleaner struct {
	calls  int
	err    error
	record state.TrackedProcessRecord
}

type runnerUnknownCleaner struct {
	runnerCleaner
	err error
}

func (cleaner *runnerUnknownCleaner) CleanupUnknown(context.Context, processpkg.Child, processpkg.ProcessMode, state.TrackedProcessRecord) (bool, error) {
	return false, cleaner.err
}

func (cleaner *runnerCleaner) Cleanup(_ context.Context, child processpkg.Child, record state.TrackedProcessRecord) error {
	cleaner.calls++
	cleaner.record = record
	if cleaner.err == nil {
		_ = child.Signal(os.Kill)
	}
	return cleaner.err
}

func (cleaner *runnerCleaner) Exists(context.Context, state.TrackedProcessRecord) (bool, error) {
	return false, nil
}

type runnerForwarder struct {
	signal os.Signal
	record state.TrackedProcessRecord
}

func (forwarder *runnerForwarder) Forward(_ context.Context, record state.TrackedProcessRecord, signal os.Signal) error {
	forwarder.record = record
	forwarder.signal = signal
	return nil
}

func TestRunnerTableDrivenContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        processpkg.ProcessMode
		status      processpkg.ExitStatus
		registerErr error
		cleanupErr  error
		wantCode    int
		wantTail    string
		wantCleanup bool
	}{
		{name: "attached exit", mode: processpkg.Attached, status: processpkg.ExitStatus{Code: 7}, wantCode: 7},
		{name: "signal maps to conventional code", mode: processpkg.Detached, status: processpkg.ExitStatus{Code: -1, Signal: syscall.SIGTERM}, wantCode: 143},
		{name: "registration cleanup", mode: processpkg.Detached, status: processpkg.ExitStatus{Code: 0}, registerErr: errors.New("state lock lost"), wantCleanup: true},
		{name: "cleanup failure is retained", mode: processpkg.Detached, status: processpkg.ExitStatus{Code: 0}, registerErr: errors.New("state lock lost"), cleanupErr: errors.New("identity unavailable"), wantCleanup: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			child := &runnerChild{pid: 42, status: test.status}
			spawner := &runnerSpawner{child: child}
			cleaner := &runnerCleaner{err: test.cleanupErr}
			describer := runnerDescriber{record: state.TrackedProcessRecord{PID: 42, StartedAt: "started", GroupID: int64Pointer(42)}}
			registered := false
			runner := processpkg.Runner{Spawner: spawner, Describer: describer, Cleaner: cleaner}
			result, err := runner.Run(context.Background(), []string{"tool", "--flag"}, processpkg.RunOptions{
				Mode: test.mode, CaptureLimit: 4,
				Register: func(_ context.Context, record state.TrackedProcessRecord) error {
					if child.waited {
						t.Fatal("registration occurred after wait")
					}
					registered = record.StartedAt == "started"
					return test.registerErr
				},
			})
			if test.registerErr != nil {
				if err == nil {
					t.Fatal("Run succeeded after registration failure")
				}
				var registrationErr *processpkg.RegistrationError
				if !errors.As(err, &registrationErr) {
					t.Fatalf("error = %T %v, want RegistrationError", err, err)
				}
				if test.cleanupErr != nil && !errors.Is(err, test.cleanupErr) {
					t.Fatalf("error = %v, want cleanup cause", err)
				}
			} else if err != nil {
				t.Fatalf("Run returned an error: %v", err)
			}
			if !registered {
				t.Fatal("registration callback was not invoked")
			}
			if cleaner.calls != boolInt(test.wantCleanup) {
				t.Fatalf("cleanup calls = %d, want %d", cleaner.calls, boolInt(test.wantCleanup))
			}
			if test.registerErr == nil && result.ExitCode != test.wantCode {
				t.Fatalf("exit code = %d, want %d", result.ExitCode, test.wantCode)
			}
			if !reflect.DeepEqual(spawner.request.Args, []string{"--flag"}) {
				t.Fatalf("spawn args = %#v", spawner.request.Args)
			}
		})
	}
}

func TestRunnerBoundsDiagnosticTailAndPreservesWriter(t *testing.T) {
	t.Parallel()
	child := &runnerChild{pid: 12, status: processpkg.ExitStatus{Code: 0}}
	spawner := &runnerSpawner{child: child, stdout: "123456"}
	describer := runnerDescriber{record: state.TrackedProcessRecord{PID: 12, StartedAt: "started"}}
	var output strings.Builder
	runner := processpkg.Runner{Spawner: spawner, Describer: describer}
	result, err := runner.Run(context.Background(), []string{"tool"}, processpkg.RunOptions{Stdout: &output, CaptureLimit: 4})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if result.Stdout != "3456" {
		t.Fatalf("captured output = %q, want bounded tail", result.Stdout)
	}
	if !strings.Contains(output.String(), "123456") {
		t.Fatalf("forwarded output = %q", output.String())
	}
}

func TestRunnerCompletesHandoffBeforeWaitingForChild(t *testing.T) {
	t.Parallel()
	child := &runnerChild{pid: 77, status: processpkg.ExitStatus{Code: 0}}
	runner := processpkg.Runner{
		Spawner:   &runnerSpawner{child: child},
		Describer: runnerDescriber{record: state.TrackedProcessRecord{PID: 77, StartedAt: "started"}},
	}
	handoffCalled := false
	handoffSawWait := false
	_, err := runner.Run(context.Background(), []string{"tool"}, processpkg.RunOptions{
		Register: func(context.Context, state.TrackedProcessRecord) error { return nil },
		HandoffComplete: func() {
			handoffCalled = true
			handoffSawWait = child.waited
		},
	})
	if err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}
	if !handoffCalled {
		t.Fatal("handoff callback was not called")
	}
	if handoffSawWait {
		t.Fatal("handoff callback ran after child wait")
	}
}

func TestRunnerPropagatesForegroundTerminalBoundary(t *testing.T) {
	t.Parallel()
	child := &runnerChild{pid: 13, status: processpkg.ExitStatus{Code: 0}}
	spawner := &runnerSpawner{child: child}
	runner := processpkg.Runner{
		Spawner:   spawner,
		Describer: runnerDescriber{record: state.TrackedProcessRecord{PID: 13, StartedAt: "started", GroupID: int64Pointer(13)}},
	}
	if _, err := runner.Run(context.Background(), []string{"shell"}, processpkg.RunOptions{Mode: processpkg.Detached, ForegroundTerminal: true}); err != nil {
		t.Fatal(err)
	}
	if !spawner.request.ForegroundTerminal || spawner.request.Mode != processpkg.Detached {
		t.Fatalf("spawn boundary = %#v", spawner.request)
	}
}

func TestNativeProcessDescriberRequiresExactIdentityAndDetachedGroup(t *testing.T) {
	t.Parallel()
	probe := staticProbe{state: lock.ProcessState{Alive: true, IdentityKnown: true, Identity: "started"}}
	table := staticTable{{PID: 42, GroupID: 42}}
	describer := processpkg.NativeProcessDescriber{Probe: probe, Table: table}
	record, err := describer.Describe(context.Background(), 42, processpkg.Detached, []string{"tool"})
	if err != nil {
		t.Fatalf("Describe returned an error: %v", err)
	}
	if record.PID != 42 || record.StartedAt != "started" {
		t.Fatalf("record = %#v", record)
	}
	if runtime.GOOS == "windows" {
		if record.GroupID != nil {
			t.Fatalf("Windows record group = %v, want nil because detached processes use a job boundary", *record.GroupID)
		}
	} else if record.GroupID == nil || *record.GroupID != 42 {
		t.Fatalf("POSIX record group = %v, want 42", record.GroupID)
	}
	if !reflect.DeepEqual(record.Command, []string{"tool"}) {
		t.Fatalf("command = %#v", record.Command)
	}
}

func TestRunnerForwardSignalHook(t *testing.T) {
	t.Parallel()
	forwarder := &runnerForwarder{}
	record := state.TrackedProcessRecord{PID: 42, StartedAt: "started"}
	if err := (processpkg.Runner{Forwarder: forwarder}).ForwardSignal(context.Background(), record, syscall.SIGINT); err != nil {
		t.Fatalf("ForwardSignal returned an error: %v", err)
	}
	if forwarder.signal != syscall.SIGINT || forwarder.record.PID != 42 {
		t.Fatalf("forwarded signal = %#v record = %#v", forwarder.signal, forwarder.record)
	}
}

func TestRunnerPostSpawnDescriptionFailureCleanupIsIdentityFenced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		record       state.TrackedProcessRecord
		cleanupErr   error
		wantCleanup  bool
		wantSetupErr bool
	}{
		{
			name:         "describer error does not trust returned attached record",
			record:       state.TrackedProcessRecord{PID: 77, StartedAt: "started"},
			wantCleanup:  false,
			wantSetupErr: true,
		},
		{
			name:         "describer error retains returned record without cleanup",
			record:       state.TrackedProcessRecord{PID: 77, StartedAt: "started"},
			wantCleanup:  false,
			wantSetupErr: true,
		},
		{
			name:         "unverified description retains pid only",
			record:       state.TrackedProcessRecord{},
			wantCleanup:  false,
			wantSetupErr: true,
		},
		{
			name:         "unusable described record retains pid only",
			record:       state.TrackedProcessRecord{PID: 77},
			wantCleanup:  false,
			wantSetupErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			child := &runnerChild{pid: 77, status: processpkg.ExitStatus{Code: 0}}
			cleaner := &runnerCleaner{err: test.cleanupErr}
			runner := processpkg.Runner{
				Spawner:   &runnerSpawner{child: child},
				Describer: runnerDescriber{record: test.record, err: errors.New("identity probe failed")},
				Cleaner:   cleaner,
			}
			result, err := runner.Run(context.Background(), []string{"tool"}, processpkg.RunOptions{Mode: processpkg.Attached})
			if err == nil {
				t.Fatal("Run succeeded after description failure")
			}
			var setupErr *processpkg.ProcessSetupError
			if errors.As(err, &setupErr) != test.wantSetupErr {
				t.Fatalf("ProcessSetupError = %v, want %v (error %v)", setupErr != nil, test.wantSetupErr, err)
			}
			if test.wantSetupErr && !test.wantCleanup {
				var unsafeErr *processpkg.ProcessCleanupUnsafeError
				if !errors.As(err, &unsafeErr) {
					t.Fatalf("error = %T %v, want ProcessCleanupUnsafeError", err, err)
				}
				if unsafeErr.PID != 77 || unsafeErr.Mode != processpkg.Attached {
					t.Fatalf("unsafe cleanup error = %#v", unsafeErr)
				}
			}
			if test.cleanupErr != nil && !errors.Is(err, test.cleanupErr) {
				t.Fatalf("error = %v, want cleanup cause", err)
			}
			if cleaner.calls != boolInt(test.wantCleanup) {
				t.Fatalf("cleanup calls = %d, want %d", cleaner.calls, boolInt(test.wantCleanup))
			}
			if result.PID != 77 {
				t.Fatalf("result PID = %d, want 77", result.PID)
			}
			if !test.wantCleanup && result.Record.StartedAt != "" {
				t.Fatalf("unverified record = %#v", result.Record)
			}
		})
	}
}

func TestRunnerValidationFailureDoesNotSignalUnverifiedDetachedGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows detached group validation is intentionally deferred")
	}
	t.Parallel()
	child := &runnerChild{pid: 88, status: processpkg.ExitStatus{Code: 0}}
	cleaner := &runnerCleaner{}
	runner := processpkg.Runner{
		Spawner:   &runnerSpawner{child: child},
		Describer: runnerDescriber{record: state.TrackedProcessRecord{PID: 88, StartedAt: "started", GroupID: int64Pointer(999)}},
		Cleaner:   cleaner,
	}
	result, err := runner.Run(context.Background(), []string{"tool"}, processpkg.RunOptions{Mode: processpkg.Detached})
	if err == nil {
		t.Fatal("Run succeeded with an inexact detached group")
	}
	if cleaner.calls != 0 {
		t.Fatalf("cleanup calls = %d, want 0 for unverified group", cleaner.calls)
	}
	if result.PID != 88 || result.Record.StartedAt != "" {
		t.Fatalf("retained result = %#v", result)
	}
}

func TestRunnerPersistsUnverifiedDetachedSentinelBeforeUnsafeCleanup(t *testing.T) {
	child := &runnerChild{pid: 91, status: processpkg.ExitStatus{Code: 0}}
	cleaner := &runnerUnknownCleaner{err: errors.New("identity unavailable")}
	runner := processpkg.Runner{
		Spawner:   &runnerSpawner{child: child},
		Describer: runnerDescriber{err: errors.New("identity probe failed")},
		Cleaner:   cleaner,
	}
	var sentinel state.TrackedProcessRecord
	_, err := runner.Run(context.Background(), []string{"tool", "--flag"}, processpkg.RunOptions{
		Mode: processpkg.Detached,
		Register: func(_ context.Context, record state.TrackedProcessRecord) error {
			sentinel = record
			return nil
		},
	})
	if err == nil || sentinel.StartedAt != processpkg.UnverifiedIdentityMarker || sentinel.PID != 91 || sentinel.GroupID == nil || *sentinel.GroupID != 91 {
		t.Fatalf("Run error = %v; sentinel = %#v", err, sentinel)
	}
	if !reflect.DeepEqual(sentinel.Command, []string{"tool", "--flag"}) {
		t.Fatalf("sentinel command = %#v", sentinel.Command)
	}
}

func TestRunnerPersistsUnverifiedAttachedSentinelWithoutGroup(t *testing.T) {
	child := &runnerChild{pid: 92, status: processpkg.ExitStatus{Code: 0}}
	cleaner := &runnerUnknownCleaner{err: errors.New("identity unavailable")}
	runner := processpkg.Runner{
		Spawner:   &runnerSpawner{child: child},
		Describer: runnerDescriber{err: errors.New("identity probe failed")},
		Cleaner:   cleaner,
	}
	var sentinel state.TrackedProcessRecord
	_, err := runner.Run(context.Background(), []string{"tool"}, processpkg.RunOptions{
		Mode: processpkg.Attached,
		Register: func(_ context.Context, record state.TrackedProcessRecord) error {
			sentinel = record
			return nil
		},
	})
	if err == nil || sentinel.StartedAt != processpkg.UnverifiedIdentityMarker || sentinel.PID != 92 || sentinel.GroupID != nil {
		t.Fatalf("Run error = %v; sentinel = %#v, want attached sentinel without group", err, sentinel)
	}
}

func TestNativeProcessCleanerAttachedRejectsIdentityChangeAtSecondFence(t *testing.T) {
	probe := &releaseProbeForRunner{states: []lock.ProcessState{
		{Alive: true, IdentityKnown: true, Identity: "started"},
		{Alive: true, IdentityKnown: true, Identity: "replacement"},
	}}
	child := &runnerChild{pid: 93}
	cleaner := processpkg.NativeProcessCleaner{Probe: probe}
	err := cleaner.Cleanup(context.Background(), child, state.TrackedProcessRecord{PID: 93, StartedAt: "started"})
	if err == nil || len(child.signals) != 0 || probe.index != 2 {
		t.Fatalf("Cleanup = %v; signals = %#v; probe calls = %d", err, child.signals, probe.index)
	}
}

func int64Pointer(value int64) *int64 { return &value }
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type releaseProbeForRunner struct {
	states []lock.ProcessState
	index  int
}

func (probe *releaseProbeForRunner) Inspect(context.Context, int) (lock.ProcessState, error) {
	if len(probe.states) == 0 {
		return lock.ProcessState{}, errors.New("probe exhausted")
	}
	state := probe.states[0]
	if len(probe.states) > 1 {
		probe.states = probe.states[1:]
	}
	probe.index++
	return state, nil
}

type staticProbe struct{ state lock.ProcessState }

func (probe staticProbe) Inspect(context.Context, int) (lock.ProcessState, error) {
	return probe.state, nil
}

type staticTable []processpkg.Entry

func (table staticTable) Snapshot(context.Context) ([]processpkg.Entry, error) { return table, nil }
