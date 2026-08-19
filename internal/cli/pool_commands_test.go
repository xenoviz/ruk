package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/lifecycle"
)

func TestWarmRejectsInvalidCountBeforeMutation(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "NaN", "9007199254740992", "18446744073709551615"} {
		t.Run(strings.ReplaceAll(value, "+", "positive-"), func(t *testing.T) {
			called := false
			_, err := cli.Warm(context.Background(), cli.WarmInput{Count: value}, func(context.Context, cli.WarmRequest) (lifecycle.WarmResult, error) {
				called = true
				return lifecycle.WarmResult{}, nil
			})
			if err == nil || !strings.Contains(err.Error(), "--count must be a positive integer") {
				t.Fatalf("error = %v", err)
			}
			if called {
				t.Fatal("warm operation was called for invalid count")
			}
		})
	}
}

func TestWarmCarriesOptionsAndRendersHumanOutput(t *testing.T) {
	called := false
	result, err := cli.Warm(context.Background(), cli.WarmInput{
		Count: "2",
		From:  "origin/main",
		Fetch: true,
	}, func(_ context.Context, request cli.WarmRequest) (lifecycle.WarmResult, error) {
		called = true
		if request.JSON {
			t.Fatal("human warm marked operation input machine-readable")
		}
		if request.Count != 2 || request.From != "origin/main" || !request.Fetch {
			t.Fatalf("warm request = %#v", request)
		}
		return lifecycle.WarmResult{
			Status:    "warmed",
			Requested: 2,
			Available: 3,
			Created:   []string{"/pool/ruk-a"},
		}, nil
	})
	if err != nil {
		t.Fatalf("Warm returned an error: %v", err)
	}
	if !called {
		t.Fatal("warm operation was not called")
	}
	if result.Output != "Available workspaces: 3 (1 created)\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestWarmRendersStableJSONAndRejectsMalformedResults(t *testing.T) {
	result, err := cli.Warm(context.Background(), cli.WarmInput{Count: "1", JSON: true}, func(_ context.Context, request cli.WarmRequest) (lifecycle.WarmResult, error) {
		if !request.JSON {
			t.Fatal("JSON warm did not mark operation input machine-readable")
		}
		return lifecycle.WarmResult{Status: "warmed", Requested: 1, Available: 1, Created: []string{}}, nil
	})
	if err != nil {
		t.Fatalf("Warm returned an error: %v", err)
	}
	const want = `{"status":"warmed","requested":1,"available":1,"created":[]}` + "\n"
	if result.Output != want {
		t.Fatalf("JSON output = %q, want %q", result.Output, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.Output), &decoded); err != nil {
		t.Fatalf("JSON output is invalid: %v", err)
	}

	for _, test := range []struct {
		name   string
		result lifecycle.WarmResult
	}{
		{name: "wrong status", result: lifecycle.WarmResult{Status: "planned", Requested: 1, Available: 1}},
		{name: "wrong requested count", result: lifecycle.WarmResult{Status: "warmed", Requested: 2, Available: 2}},
		{name: "insufficient available", result: lifecycle.WarmResult{Status: "warmed", Requested: 1, Available: 0}},
		{name: "too many created", result: lifecycle.WarmResult{Status: "warmed", Requested: 1, Available: 1, Created: []string{"a", "b"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := cli.Warm(context.Background(), cli.WarmInput{Count: "1", JSON: true}, func(context.Context, cli.WarmRequest) (lifecycle.WarmResult, error) {
				return test.result, nil
			})
			if err == nil {
				t.Fatal("Warm accepted malformed result")
			}
			if got.Output != "" {
				t.Fatalf("malformed result produced output %q", got.Output)
			}
		})
	}

	_, err = cli.Warm(context.Background(), cli.WarmInput{Count: "1"}, func(context.Context, cli.WarmRequest) (lifecycle.WarmResult, error) {
		return lifecycle.WarmResult{}, errors.New("warm failed")
	})
	if err == nil || err.Error() != "warm failed" {
		t.Fatalf("operation error = %v", err)
	}
}

func TestGCRejectsInvalidOptionsBeforeMutation(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	for _, value := range []string{"-1", "NaN", "+Inf", "-Inf", "not-a-number", "1e20"} {
		t.Run(strings.ReplaceAll(value, "+", "positive-"), func(t *testing.T) {
			called := false
			_, err := cli.GC(context.Background(), cli.GCInput{MaxAgeMinutes: value, Now: now}, func(context.Context, cli.GCRequest) (lifecycle.GCResult, error) {
				called = true
				return lifecycle.GCResult{}, nil
			})
			if err == nil {
				t.Fatal("GC accepted invalid max age")
			}
			if called {
				t.Fatal("GC operation was called for invalid max age")
			}
		})
	}

	called := false
	_, err := cli.GC(context.Background(), cli.GCInput{ForceExpired: true, Now: now}, func(context.Context, cli.GCRequest) (lifecycle.GCResult, error) {
		called = true
		return lifecycle.GCResult{}, nil
	})
	if err == nil || err.Error() != "--force-expired requires --apply" {
		t.Fatalf("force-expired error = %v", err)
	}
	if called {
		t.Fatal("GC operation was called without --apply")
	}
}

func TestGCCarriesFlagsUsesDefaultCutoffAndRendersHumanOutput(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result, err := cli.GC(context.Background(), cli.GCInput{
		Apply:                false,
		ForceExpired:         false,
		CurrentWorkspacePath: "/pool/current",
		Now:                  now,
	}, func(_ context.Context, request cli.GCRequest) (lifecycle.GCResult, error) {
		options := request.Options
		if options.Apply || options.ForceExpired || options.CurrentWorkspacePath != "/pool/current" {
			t.Fatalf("GC options = %#v", options)
		}
		if !options.Now.Equal(now) || !options.OlderThan.Equal(now.Add(-1440*time.Minute)) {
			t.Fatalf("GC times = now %s, cutoff %s", options.Now, options.OlderThan)
		}
		return lifecycle.GCResult{
			Status: "planned",
			Removed: []lifecycle.GCRemovedRecord{
				{Path: "/pool/old-a", Lifecycle: "available", Reason: "older than max age"},
				{Path: "/pool/old-b", Lifecycle: "prepared", Reason: "abandoned preparation"},
			},
			Expired: []lifecycle.GCExpiredRecord{{Path: "/pool/expired", AssignmentID: "assignment-1", ExpiresAt: "2026-08-15T12:00:00.000Z"}},
		}, nil
	})
	if err != nil {
		t.Fatalf("GC returned an error: %v", err)
	}
	if result.Output != "Would collect: 2 workspace(s)\nExpired assignments: 1\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestGCRendersJSONAndRejectsFailuresWithoutSuccessOutput(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	result, err := cli.GC(context.Background(), cli.GCInput{MaxAgeMinutes: "30", Apply: true, ForceExpired: true, JSON: true, Now: now}, func(_ context.Context, request cli.GCRequest) (lifecycle.GCResult, error) {
		if !request.Options.Apply || !request.Options.ForceExpired || !request.Options.OlderThan.Equal(now.Add(-30*time.Minute)) {
			t.Fatalf("GC options = %#v", request.Options)
		}
		return lifecycle.GCResult{
			Status:  "collected",
			Removed: []lifecycle.GCRemovedRecord{{Path: "/pool/old", Lifecycle: "available", Reason: "older than max age"}},
			Expired: []lifecycle.GCExpiredRecord{},
		}, nil
	})
	if err != nil {
		t.Fatalf("GC returned an error: %v", err)
	}
	const want = `{"status":"collected","removed":[{"path":"/pool/old","lifecycle":"available","reason":"older than max age"}],"expired":[]}` + "\n"
	if result.Output != want {
		t.Fatalf("JSON output = %q, want %q", result.Output, want)
	}

	for _, malformed := range []lifecycle.GCResult{
		{Status: "planned"},
		{Status: "collected", Removed: []lifecycle.GCRemovedRecord{{Path: "/pool/old", Lifecycle: "", Reason: "old"}}},
		{Status: "collected", Expired: []lifecycle.GCExpiredRecord{{Path: "/pool/old", AssignmentID: "", ExpiresAt: "later"}}},
	} {
		got, err := cli.GC(context.Background(), cli.GCInput{Apply: true, Now: now}, func(context.Context, cli.GCRequest) (lifecycle.GCResult, error) {
			return malformed, nil
		})
		if err == nil || got.Output != "" {
			t.Fatalf("malformed GC result: result=%#v error=%v", got, err)
		}
	}

	got, err := cli.GC(context.Background(), cli.GCInput{Now: now}, func(context.Context, cli.GCRequest) (lifecycle.GCResult, error) {
		return lifecycle.GCResult{}, errors.New("gc failed")
	})
	if err == nil || err.Error() != "gc failed" || got.Output != "" {
		t.Fatalf("operation failure: result=%#v error=%v", got, err)
	}
}
