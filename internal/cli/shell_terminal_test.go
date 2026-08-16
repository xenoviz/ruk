package cli_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

type shellProcessRunnerStub struct {
	command []string
	options processpkg.RunOptions
	result  processpkg.RunResult
	err     error
}

func (stub *shellProcessRunnerStub) Run(_ context.Context, command []string, options processpkg.RunOptions) (processpkg.RunResult, error) {
	stub.command = append([]string(nil), command...)
	stub.options = options
	if stub.err == nil && options.Register != nil {
		if err := options.Register(context.Background(), stub.result.Record); err != nil {
			return stub.result, err
		}
	}
	return stub.result, stub.err
}

type shellProcessTrackerStub struct {
	record state.TrackedProcessRecord
	alive  bool
	err    error
}

func (stub *shellProcessTrackerStub) Exists(_ context.Context, record state.TrackedProcessRecord) (bool, error) {
	stub.record = record
	return stub.alive, stub.err
}

func shellTerminalRequest() cli.ShellTerminalRequest {
	return cli.ShellTerminalRequest{
		AssignmentID:  "assignment-1",
		WorkspacePath: "/workspace/one",
		Shell:         "/bin/sh",
		Environment:   map[string]string{"RUK_PORT_HTTP": "4312"},
		Stdin:         strings.NewReader("input"),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
	}
}

func shellProcessRecord() state.TrackedProcessRecord {
	groupID := int64(42)
	return state.TrackedProcessRecord{PID: 42, GroupID: &groupID, StartedAt: "identity-42"}
}

func shellTerminalOptions(runner cli.ShellProcessRunner, tracker cli.ShellProcessTracker) cli.ShellTerminalOptions {
	return cli.ShellTerminalOptions{
		Runner: runner, Tracker: tracker,
		Register: func(context.Context, string, state.TrackedProcessRecord) error { return nil },
		Remove:   func(context.Context, string, state.TrackedProcessRecord) error { return nil },
	}
}

func TestNativeShellTerminalRunsForegroundNativeProcessAndDrainsIt(t *testing.T) {
	runner := &shellProcessRunnerStub{result: processpkg.RunResult{
		ExitCode: 130,
		Signal:   nativeShellSignal("interrupt"),
		Record:   shellProcessRecord(),
	}}
	tracker := &shellProcessTrackerStub{}
	terminal := cli.NewNativeShellTerminal(shellTerminalOptions(runner, tracker))
	request := shellTerminalRequest()
	parentPort := os.Getenv("RUK_PORT_HTTP")

	result, err := terminal.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.DescendantsDrained || result.ExitCode != 130 || result.Signal == nil {
		t.Fatalf("result = %#v", result)
	}
	if len(runner.command) != 1 || runner.command[0] != "/bin/sh" {
		t.Fatalf("command = %v", runner.command)
	}
	if runner.options.Dir != "/workspace/one" || runner.options.Mode != processpkg.Detached || !runner.options.ForegroundTerminal {
		t.Fatalf("run options = %#v", runner.options)
	}
	portFound := false
	for _, entry := range runner.options.Env {
		if entry == "RUK_PORT_HTTP=4312" {
			portFound = true
			break
		}
	}
	if !portFound {
		t.Fatalf("run environment does not contain allocated port: %v", runner.options.Env)
	}
	if got := os.Getenv("RUK_PORT_HTTP"); got != parentPort {
		t.Fatalf("parent port environment changed from %q to %q", parentPort, got)
	}
	if runner.options.Stdin != request.Stdin || runner.options.Stdout != request.Stdout || runner.options.Stderr != request.Stderr {
		t.Fatal("native terminal did not preserve stdio")
	}
	if tracker.record.PID != 42 || tracker.record.StartedAt != "identity-42" {
		t.Fatalf("tracker record = %#v", tracker.record)
	}
}

func TestNativeShellTerminalFailsClosedWhenDescendantsRemain(t *testing.T) {
	runner := &shellProcessRunnerStub{result: processpkg.RunResult{Record: shellProcessRecord()}}
	tracker := &shellProcessTrackerStub{alive: true}
	terminal := cli.NewNativeShellTerminal(shellTerminalOptions(runner, tracker))

	result, err := terminal.Run(context.Background(), shellTerminalRequest())
	if err == nil || result.DescendantsDrained {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if !strings.Contains(err.Error(), "descendants") {
		t.Fatalf("error = %v", err)
	}
}

func TestNativeShellTerminalRegistersUntilTheExactTreeDrains(t *testing.T) {
	record := shellProcessRecord()
	runner := &shellProcessRunnerStub{result: processpkg.RunResult{Record: record}}
	tracker := &shellProcessTrackerStub{}
	events := []string{}
	terminal := cli.NewNativeShellTerminal(cli.ShellTerminalOptions{
		Runner:  runner,
		Tracker: tracker,
		Register: func(_ context.Context, assignmentID string, registered state.TrackedProcessRecord) error {
			events = append(events, "register:"+assignmentID)
			if registered.PID != record.PID || registered.StartedAt != record.StartedAt {
				t.Fatalf("registered record = %#v", registered)
			}
			return nil
		},
		Remove: func(_ context.Context, assignmentID string, removed state.TrackedProcessRecord) error {
			events = append(events, "remove:"+assignmentID)
			if removed.PID != record.PID || removed.StartedAt != record.StartedAt {
				t.Fatalf("removed record = %#v", removed)
			}
			return nil
		},
	})

	result, err := terminal.Run(context.Background(), shellTerminalRequest())
	if err != nil || !result.DescendantsDrained {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if got := strings.Join(events, ","); got != "register:assignment-1,remove:assignment-1" {
		t.Fatalf("registration events = %q", got)
	}
}

func TestNativeShellTerminalForwardsPendingManagedSignals(t *testing.T) {
	record := shellProcessRecord()
	runner := &shellProcessRunnerStub{result: processpkg.RunResult{Record: record}}
	forwarder := &shellSignalForwarderStub{}
	signals := make(chan os.Signal, 1)
	signals <- os.Interrupt
	options := shellTerminalOptions(runner, &shellProcessTrackerStub{})
	options.Forwarder = forwarder
	options.Signals = signals
	terminal := cli.NewNativeShellTerminal(options)

	result, err := terminal.Run(context.Background(), shellTerminalRequest())
	if err != nil || !result.DescendantsDrained {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if len(forwarder.signals) != 1 || forwarder.signals[0] != os.Interrupt || forwarder.record.PID != record.PID {
		t.Fatalf("forwarded signals=%v record=%#v", forwarder.signals, forwarder.record)
	}
}

func TestNativeShellTerminalFailsClosedOnTrackerError(t *testing.T) {
	trackerErr := errors.New("identity unavailable")
	runner := &shellProcessRunnerStub{result: processpkg.RunResult{Record: shellProcessRecord()}}
	tracker := &shellProcessTrackerStub{err: trackerErr}
	terminal := cli.NewNativeShellTerminal(shellTerminalOptions(runner, tracker))

	_, err := terminal.Run(context.Background(), shellTerminalRequest())
	if !errors.Is(err, trackerErr) {
		t.Fatalf("error = %v, want %v", err, trackerErr)
	}
}

func TestNativeShellTerminalFailsClosedOnRunnerErrorOrInexactRecord(t *testing.T) {
	for _, test := range []struct {
		name   string
		runner *shellProcessRunnerStub
	}{
		{name: "runner error", runner: &shellProcessRunnerStub{err: errors.New("spawn failed")}},
		{name: "missing identity", runner: &shellProcessRunnerStub{result: processpkg.RunResult{Record: state.TrackedProcessRecord{PID: 42}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := cli.NewNativeShellTerminal(shellTerminalOptions(test.runner, &shellProcessTrackerStub{}))
			result, err := terminal.Run(context.Background(), shellTerminalRequest())
			if err == nil || result.DescendantsDrained {
				t.Fatalf("result/error = %#v/%v", result, err)
			}
		})
	}
}

func TestNativeShellTerminalValidatesRequestBeforeSpawning(t *testing.T) {
	runner := &shellProcessRunnerStub{}
	terminal := cli.NewNativeShellTerminal(shellTerminalOptions(runner, &shellProcessTrackerStub{}))
	request := shellTerminalRequest()
	request.AssignmentID = ""

	_, err := terminal.Run(context.Background(), request)
	if err == nil || runner.command != nil {
		t.Fatalf("error/command = %v/%v", err, runner.command)
	}
}

type nativeShellSignal string

func (signal nativeShellSignal) String() string { return string(signal) }
func (nativeShellSignal) Signal()               {}

type shellSignalForwarderStub struct {
	record  state.TrackedProcessRecord
	signals []os.Signal
}

func (forwarder *shellSignalForwarderStub) ForwardSignal(_ context.Context, record state.TrackedProcessRecord, signal os.Signal) error {
	forwarder.record = record
	forwarder.signals = append(forwarder.signals, signal)
	return nil
}
