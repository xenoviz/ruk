package cli_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
)

type shellSignal string

func (signal shellSignal) String() string { return string(signal) }
func (shellSignal) Signal()               {}

type shellTerminalStub struct {
	request cli.ShellTerminalRequest
	result  cli.ShellTerminalResult
	err     error
	called  bool
	events  *[]string
}

func (stub *shellTerminalStub) Run(_ context.Context, request cli.ShellTerminalRequest) (cli.ShellTerminalResult, error) {
	stub.called = true
	stub.request = request
	if stub.events != nil {
		*stub.events = append(*stub.events, "terminal")
	}
	return stub.result, stub.err
}

func validShellAcquire() cli.AcquireResult {
	return cli.AcquireResult{AcquireRecord: cli.AcquireRecord{
		Status:       "assigned",
		AssignmentID: "assignment-1",
		Path:         "/workspaces/one",
		Branch:       "feature/shell",
		ExpiresAt:    "2026-08-16T12:00:00Z",
	}}
}

func shellInput() cli.ShellInput {
	return cli.ShellInput{
		Branch:   "feature/shell",
		TTL:      "30",
		Owner:    "owner-1",
		Hostname: "host-1",
		Now:      time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC),
		Environment: map[string]string{
			"RUK_SHELL": "/custom/shell",
		},
		Platform: "linux",
	}
}

func TestShellReleasesAfterDrainedTerminalAndPreservesStdioAndStatus(t *testing.T) {
	stdin := bytes.NewBufferString("input")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	events := []string{}
	acquireCalls := 0
	releaseCalls := 0
	terminal := &shellTerminalStub{
		result: cli.ShellTerminalResult{
			ExitCode:           130,
			Signal:             shellSignal("interrupt"),
			DescendantsDrained: true,
		},
		events: &events,
	}
	service := cli.NewShellService(cli.ShellOptions{
		Acquire: func(_ context.Context, input cli.AcquireInput) (cli.AcquireResult, error) {
			acquireCalls++
			if input.Owner != "owner-1" || input.Hostname != "host-1" {
				t.Fatalf("acquire input = %#v", input)
			}
			return validShellAcquire(), nil
		},
		Terminal: terminal,
		Release: func(_ context.Context, assignmentID string) error {
			releaseCalls++
			events = append(events, "release")
			if assignmentID != "assignment-1" {
				t.Fatalf("released assignment = %q", assignmentID)
			}
			return nil
		},
	})

	input := shellInput()
	input.Stdin, input.Stdout, input.Stderr = stdin, stdout, stderr
	result, err := service.Shell(context.Background(), input)
	if err != nil {
		t.Fatalf("Shell() error = %v", err)
	}
	if acquireCalls != 1 || releaseCalls != 1 {
		t.Fatalf("acquire/release calls = %d/%d", acquireCalls, releaseCalls)
	}
	if got, want := events, []string{"terminal", "release"}; !equalStrings(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if result.Shell != "/custom/shell" || result.ExitCode != 130 || result.Signal == nil || !result.Released || result.Retained {
		t.Fatalf("result = %#v", result)
	}
	if terminal.request.AssignmentID != "assignment-1" || terminal.request.WorkspacePath != "/workspaces/one" || terminal.request.Shell != "/custom/shell" {
		t.Fatalf("terminal request = %#v", terminal.request)
	}
	if terminal.request.Stdin != stdin || terminal.request.Stdout != stdout || terminal.request.Stderr != stderr {
		t.Fatal("terminal did not receive the caller's stdio")
	}
	if stderr.String() != "Shell workspace: /workspaces/one\nAssignment: assignment-1\n" {
		t.Fatalf("shell handoff diagnostic = %q", stderr.String())
	}
}

func TestShellReturnsAcquireFailureWithoutStartingOrReleasing(t *testing.T) {
	wantErr := errors.New("no workspace available")
	terminal := &shellTerminalStub{}
	releaseCalls := 0
	service := cli.NewShellService(cli.ShellOptions{
		Acquire: func(context.Context, cli.AcquireInput) (cli.AcquireResult, error) {
			return cli.AcquireResult{}, wantErr
		},
		Terminal: terminal,
		Release: func(context.Context, string) error {
			releaseCalls++
			return nil
		},
	})

	_, err := service.Shell(context.Background(), shellInput())
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if terminal.called || releaseCalls != 0 {
		t.Fatalf("terminal/release = %v/%d", terminal.called, releaseCalls)
	}
}

func TestShellRetainsAssignmentWhenTerminalFails(t *testing.T) {
	terminalErr := errors.New("terminal identity unavailable")
	terminal := &shellTerminalStub{
		result: cli.ShellTerminalResult{ExitCode: 143, Signal: shellSignal("terminate")},
		err:    terminalErr,
	}
	releaseCalls := 0
	service := cli.NewShellService(cli.ShellOptions{
		Acquire:  func(context.Context, cli.AcquireInput) (cli.AcquireResult, error) { return validShellAcquire(), nil },
		Terminal: terminal,
		Release:  func(context.Context, string) error { releaseCalls++; return nil },
	})

	result, err := service.Shell(context.Background(), shellInput())
	if err == nil || !result.Retained || result.Released || releaseCalls != 0 {
		t.Fatalf("result/error/release = %#v/%v/%d", result, err, releaseCalls)
	}
	var retained *cli.RetainedShellError
	if !errors.As(err, &retained) {
		t.Fatalf("error type = %T, want retained shell error", err)
	}
	for _, fragment := range []string{"/workspaces/one", "assignment-1", "2026-08-16T12:00:00Z", "terminal identity unavailable", "ruk release assignment-1"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error %q does not contain %q", err, fragment)
		}
	}
	if result.ExitCode != 143 || result.Signal == nil {
		t.Fatalf("terminal status was not preserved: %#v", result)
	}
}

func TestShellRetainsAssignmentWhenDescendantsRemain(t *testing.T) {
	terminal := &shellTerminalStub{result: cli.ShellTerminalResult{ExitCode: 0, DescendantsDrained: false}}
	releaseCalls := 0
	service := cli.NewShellService(cli.ShellOptions{
		Acquire:  func(context.Context, cli.AcquireInput) (cli.AcquireResult, error) { return validShellAcquire(), nil },
		Terminal: terminal,
		Release:  func(context.Context, string) error { releaseCalls++; return nil },
	})

	result, err := service.Shell(context.Background(), shellInput())
	if err == nil || !result.Retained || result.Released || releaseCalls != 0 {
		t.Fatalf("result/error/release = %#v/%v/%d", result, err, releaseCalls)
	}
	if !strings.Contains(err.Error(), "terminal descendants are still running") {
		t.Fatalf("error = %v", err)
	}
}

func TestShellRetainsAssignmentWhenReleaseFails(t *testing.T) {
	releaseErr := errors.New("release fence changed")
	terminal := &shellTerminalStub{result: cli.ShellTerminalResult{DescendantsDrained: true}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := cli.NewShellService(cli.ShellOptions{
		Acquire:  func(context.Context, cli.AcquireInput) (cli.AcquireResult, error) { return validShellAcquire(), nil },
		Terminal: terminal,
		Release: func(releaseContext context.Context, assignmentID string) error {
			if releaseContext.Err() != nil {
				t.Errorf("release context is canceled: %v", releaseContext.Err())
			}
			if assignmentID != "assignment-1" {
				t.Errorf("assignmentID = %q", assignmentID)
			}
			return releaseErr
		},
	})

	result, err := service.Shell(ctx, shellInput())
	if err == nil || !result.Retained || result.Released {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if !errors.Is(err, releaseErr) || !strings.Contains(err.Error(), "ruk release assignment-1") {
		t.Fatalf("error = %v", err)
	}
}

func TestShellRejectsInvalidTTLBeforeAcquire(t *testing.T) {
	acquireCalls := 0
	service := cli.NewShellService(cli.ShellOptions{
		Acquire: func(context.Context, cli.AcquireInput) (cli.AcquireResult, error) {
			acquireCalls++
			return validShellAcquire(), nil
		},
		Terminal: &shellTerminalStub{},
		Release:  func(context.Context, string) error { return nil },
	})

	input := shellInput()
	input.TTL = "not-a-duration"
	if _, err := service.Shell(context.Background(), input); err == nil {
		t.Fatal("Shell() accepted invalid TTL")
	}
	if acquireCalls != 0 {
		t.Fatalf("acquire calls = %d, want 0", acquireCalls)
	}
}

func TestSelectShellEnvironmentPrecedenceAndFallback(t *testing.T) {
	if got, _ := cli.SelectShell(map[string]string{
		"RUK_SHELL": "/ruk-shell",
		"SHELL":     "/shell",
		"COMSPEC":   "cmd",
	}, "linux"); got != "/ruk-shell" {
		t.Fatalf("RUK_SHELL selection = %q", got)
	}
	if got, _ := cli.SelectShell(map[string]string{"SHELL": "/shell", "COMSPEC": "cmd"}, "linux"); got != "/shell" {
		t.Fatalf("SHELL selection = %q", got)
	}
	if got, _ := cli.SelectShell(map[string]string{"COMSPEC": "cmd"}, "linux"); got != "cmd" {
		t.Fatalf("COMSPEC selection = %q", got)
	}
	if got, _ := cli.SelectShell(nil, "linux"); got != "/bin/sh" {
		t.Fatalf("unix fallback = %q", got)
	}
	if got, _ := cli.SelectShell(nil, "windows"); got != "cmd.exe" {
		t.Fatalf("windows fallback = %q", got)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
