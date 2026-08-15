package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
)

func TestClassifyErrorPreservesStableCategories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		code      ErrorCode
		retryable bool
	}{
		{name: "dirty workspace", err: errors.New("Workspace has uncommitted changes."), code: WorkspaceDirtyCode},
		{name: "invalid config", err: errors.New("Cannot read /repo/.rukrc.json: malformed JSON"), code: InvalidArgumentCode},
		{name: "unknown option", err: errors.New("Unknown option --force"), code: InvalidArgumentCode},
		{name: "dependency message", err: errors.New("Dependency installation failed"), code: DependencyPreparationCode, retryable: true},
		{name: "typed dependency", err: &dependencies.DependencyPreparationError{Cause: errors.New("installer exited")}, code: DependencyPreparationCode, retryable: true},
		{name: "port", err: errors.New("Could not allocate an available port"), code: PortUnavailableCode, retryable: true},
		{name: "assignment conflict", err: errors.New("Assignment a does not exist"), code: AssignmentConflictCode},
		{name: "lock timeout", err: &lock.TimeoutError{Path: "/tmp/lock"}, code: ResourceBusyCode, retryable: true},
		{name: "process identity", err: &processpkg.IdentityUnavailableError{PID: 42, Cause: errors.New("identity unavailable")}, code: ResourceBusyCode, retryable: true},
		{name: "process enumeration", err: errors.New("Could not enumerate POSIX processes: unavailable"), code: ResourceBusyCode, retryable: true},
		{name: "git", err: errors.New("git fetch origin main: remote rejected"), code: GitOperationFailedCode},
		{name: "generic", err: errors.New("unexpected"), code: OperationFailedCode},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := ClassifyError(test.err)
			if record.Status != "error" {
				t.Fatalf("status = %q, want error", record.Status)
			}
			if record.Code != test.code {
				t.Fatalf("code = %q, want %q", record.Code, test.code)
			}
			if record.Retryable != test.retryable {
				t.Fatalf("retryable = %t, want %t", record.Retryable, test.retryable)
			}
		})
	}
}

func TestClassifyErrorPreservesRecoveryMetadata(t *testing.T) {
	t.Parallel()

	retained := RetainedAssignmentFailure(
		"assignment-1",
		"/workspace",
		"2026-08-15T00:00:00.000Z",
		&processpkg.IdentityUnavailableError{PID: 42},
	)
	if retained == nil {
		t.Fatal("RetainedAssignmentFailure returned nil")
	}
	record := ClassifyError(retained)
	if record.Code != ResourceBusyCode || !record.Retryable {
		t.Fatalf("record = %#v, want retryable resource busy", record)
	}
	if record.AssignmentID != "assignment-1" || record.Path != "/workspace" || record.ExpiresAt != "2026-08-15T00:00:00.000Z" {
		t.Fatalf("recovery metadata = %#v", record)
	}
	if record.Recovery != "ruk release assignment-1" {
		t.Fatalf("recovery = %q", record.Recovery)
	}
	if RetainedAssignmentFailure("id", "/workspace", "expiry", errors.New("ordinary failure")) != nil {
		t.Fatal("ordinary failure was incorrectly retained")
	}

	shared := ClassifyError(NewSharedCheckoutError(2))
	if shared.ActiveAssignments != 2 || shared.Recovery != "ruk acquire <branch>" {
		t.Fatalf("shared checkout metadata = %#v", shared)
	}
}

func TestClassifyErrorSearchesJoinedCauses(t *testing.T) {
	t.Parallel()

	err := errors.Join(
		errors.New("heartbeat failed"),
		NewAssignmentActivityError("assignment-1", errors.New("EPERM")),
	)
	record := ClassifyError(err)
	if record.Code != ResourceBusyCode || !record.Retryable {
		t.Fatalf("record = %#v, want retryable resource busy", record)
	}

	if got := ClassifyError(errors.Join(
		&dependencies.DependencyPreparationError{Cause: errors.New("dependency preparation aborted")},
		&processpkg.IdentityUnavailableError{PID: 42},
	)); got.Code != DependencyPreparationCode {
		t.Fatalf("joined typed dependency code = %q, want %q", got.Code, DependencyPreparationCode)
	}
}

func TestErrorFormattingAndJSONRequested(t *testing.T) {
	t.Parallel()

	err := errors.New("Workspace has uncommitted changes.")
	json := FormatJSONError(err)
	if !strings.HasSuffix(json, "\n") || !strings.Contains(json, `"code":"WORKSPACE_DIRTY"`) {
		t.Fatalf("JSON = %q", json)
	}
	if got := FormatHumanError(err); got != "ruk: Workspace has uncommitted changes.\n" {
		t.Fatalf("human = %q", got)
	}

	tests := []struct {
		args []string
		want bool
	}{
		{args: []string{"status", "--json"}, want: true},
		{args: []string{"exec", "branch", "--", "tool", "--json"}, want: false},
		{args: []string{"run", "tool", "--json"}, want: false},
		{args: []string{"list", "--json", "--", "ignored"}, want: true},
		{args: []string{"list", "--", "--json"}, want: false},
	}
	for _, test := range tests {
		if got := JSONRequested(test.args); got != test.want {
			t.Errorf("JSONRequested(%q) = %t, want %t", test.args, got, test.want)
		}
	}
}
