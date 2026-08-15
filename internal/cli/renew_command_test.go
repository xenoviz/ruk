package cli_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/state"
)

func TestRenewCommandPreservesLeaseAndOutputContracts(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		ttl        string
		json       bool
		wantExpiry time.Time
		wantOutput string
	}{
		{
			name:       "default eight hour lease",
			wantExpiry: now.Add(8 * time.Hour),
			wantOutput: "Renewed assignment-1 until 2026-08-16T20:00:00.000Z\n",
		},
		{
			name:       "fractional JSON lease",
			ttl:        "1.5",
			json:       true,
			wantExpiry: now.Add(90 * time.Second),
			wantOutput: `{"status":"renewed","assignmentId":"assignment-1","path":"/pool/repo-ruk-a","expiresAt":"2026-08-16T12:01:30.000Z"}` + "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := 0
			operation := cli.RenewOperation(func(_ context.Context, assignmentID string, expiresAt time.Time) (state.WorkspaceRecord, error) {
				called++
				if assignmentID != "assignment-1" {
					t.Fatalf("assignment ID = %q", assignmentID)
				}
				if !expiresAt.Equal(test.wantExpiry) {
					t.Fatalf("expiry = %s, want %s", expiresAt, test.wantExpiry)
				}
				expiry := expiresAt.UTC().Format("2006-01-02T15:04:05.000Z")
				return state.WorkspaceRecord{
					Path:       "/pool/repo-ruk-a",
					Assignment: &state.AssignmentRecord{ID: assignmentID, ExpiresAt: expiry},
				}, nil
			})
			result, err := cli.Renew(context.Background(), cli.RenewInput{
				AssignmentID: "assignment-1", TTL: test.ttl, JSON: test.json, Now: now,
			}, operation)
			if err != nil {
				t.Fatalf("Renew returned an error: %v", err)
			}
			if called != 1 || result.Output != test.wantOutput {
				t.Fatalf("called=%d output=%q, want %q", called, result.Output, test.wantOutput)
			}
			if test.json {
				var record cli.RenewRecord
				if err := json.Unmarshal([]byte(result.Output), &record); err != nil {
					t.Fatalf("JSON output: %v", err)
				}
				if record.Status != "renewed" || record.AssignmentID != "assignment-1" {
					t.Fatalf("record = %#v", record)
				}
			}
		})
	}
}

func TestRenewCommandRejectsInvalidTTLBeforeMutation(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for _, ttl := range []string{"0", "-1", "NaN", "+Inf", "1e300"} {
		t.Run(strings.ReplaceAll(ttl, "+", "positive-"), func(t *testing.T) {
			called := false
			_, err := cli.Renew(context.Background(), cli.RenewInput{
				AssignmentID: "assignment-1", TTL: ttl, Now: now,
			}, func(context.Context, string, time.Time) (state.WorkspaceRecord, error) {
				called = true
				return state.WorkspaceRecord{}, nil
			})
			if err == nil || err.Error() != "--ttl must be a positive number of minutes" {
				t.Fatalf("error = %v", err)
			}
			if called {
				t.Fatal("renew operation was called for invalid TTL")
			}
		})
	}

	if _, err := cli.FutureExpiry(now, math.SmallestNonzeroFloat64); err == nil {
		t.Fatal("sub-millisecond duration must not produce a non-future expiry")
	}
}

func TestRenewCommandRejectsMalformedLifecycleResult(t *testing.T) {
	_, err := cli.Renew(context.Background(), cli.RenewInput{
		AssignmentID: "assignment-1", Now: time.Now(),
	}, func(context.Context, string, time.Time) (state.WorkspaceRecord, error) {
		return state.WorkspaceRecord{Path: "/pool/repo-ruk-a"}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "without an assignment") {
		t.Fatalf("error = %v", err)
	}
}
