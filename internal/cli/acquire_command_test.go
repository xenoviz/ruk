package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

func acquireLifecycleResult() lifecycle.AcquisitionResult {
	return lifecycle.AcquisitionResult{
		Workspace: state.WorkspaceRecord{
			Path:      "/pool/slot-a",
			Branch:    "agent/task",
			Lifecycle: state.LifecycleAssigned,
			Assignment: &state.AssignmentRecord{
				ID:        "assignment-1",
				ExpiresAt: "2026-08-16T20:00:00.000Z",
				Ports:     map[string]int64{"web": 4000, "api": 3000},
			},
		},
		Path:        "/pool/slot-a",
		Branch:      "agent/task",
		Reused:      true,
		Fingerprint: "fingerprint",
		Mode:        "managed-install",
	}
}

func TestAcquireJSONValidatesTTLAndPassesStructuredOperationInput(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	called := false
	result, err := cli.Acquire(context.Background(), cli.AcquireInput{
		Branch: "agent/task", From: "origin/main", Fetch: true, TTL: "480", Owner: "explicit-owner",
		Hostname: "test-host", Ports: []string{"web", "api"}, JSON: true, Now: now,
	}, func(_ context.Context, input cli.AcquireOperationInput) (lifecycle.AcquisitionResult, error) {
		called = true
		if input.Branch != "agent/task" || input.From != "origin/main" || input.StartPoint != "origin/main" || !input.Fetch || input.Owner != "explicit-owner" || input.Hostname != "test-host" {
			t.Fatalf("operation input = %#v", input)
		}
		if !input.ExpiresAt.Equal(now.Add(8 * time.Hour)) {
			t.Fatalf("operation expiry = %s", input.ExpiresAt)
		}
		return acquireLifecycleResult(), nil
	})
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	if !called {
		t.Fatal("acquire operation was not called")
	}
	want := `{"status":"assigned","assignmentId":"assignment-1","path":"/pool/slot-a","branch":"agent/task","expiresAt":"2026-08-16T20:00:00.000Z","reused":true,"fingerprint":"fingerprint","mode":"managed-install","ports":{"api":3000,"web":4000}}` + "\n"
	if result.Output != want {
		t.Fatalf("JSON output = %q, want %q", result.Output, want)
	}
}

func TestAcquireDefaultTTLAndOwnerFallbackRenderSortedHumanPorts(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	result, err := cli.Acquire(context.Background(), cli.AcquireInput{
		Branch: "agent/task", Now: now, OwnerFallback: func() string { return "fallback-owner" },
		HostnameFallback: func() string { return "fallback-host" },
	}, func(_ context.Context, input cli.AcquireOperationInput) (lifecycle.AcquisitionResult, error) {
		if input.Owner != "fallback-owner" || input.Hostname != "fallback-host" || !input.ExpiresAt.Equal(now.Add(8*time.Hour)) {
			t.Fatalf("fallback operation input = %#v", input)
		}
		return acquireLifecycleResult(), nil
	})
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	want := "Assigned /pool/slot-a\nAssignment: assignment-1\nExpires: 2026-08-16T20:00:00.000Z\napi: 3000\nweb: 4000\n"
	if result.Output != want {
		t.Fatalf("human output = %q, want %q", result.Output, want)
	}
}

func TestAcquireInvalidTTLDoesNotCallOperation(t *testing.T) {
	called := false
	_, err := cli.Acquire(context.Background(), cli.AcquireInput{
		Branch: "agent/task", TTL: "not-a-number", Now: time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC),
		Owner: "owner", Hostname: "host",
	}, func(context.Context, cli.AcquireOperationInput) (lifecycle.AcquisitionResult, error) {
		called = true
		return acquireLifecycleResult(), nil
	})
	if err == nil || !strings.Contains(err.Error(), "--ttl must be a positive number of minutes") {
		t.Fatalf("Acquire error = %v", err)
	}
	if called {
		t.Fatal("invalid TTL called the acquisition operation")
	}
}

func TestAcquireRejectsMalformedLifecycleResult(t *testing.T) {
	_, err := cli.Acquire(context.Background(), cli.AcquireInput{
		Branch: "agent/task", Owner: "owner", Hostname: "host",
		Now: time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC),
	}, func(context.Context, cli.AcquireOperationInput) (lifecycle.AcquisitionResult, error) {
		return lifecycle.AcquisitionResult{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "without an assignment") {
		t.Fatalf("Acquire error = %v", err)
	}
}
