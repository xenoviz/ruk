package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
)

const defaultGCMaxAgeMinutes = 1440

const maxSafeInt = uint64(1<<53 - 1)

// WarmRequest is the command-level handoff to the injected warm operation.
// From and Fetch remain separate so repository-aware adapters can resolve and
// optionally fetch the requested start point without CLI-side mutation.
type WarmRequest struct {
	Count int
	From  string
	Fetch bool
}

// WarmOperation is the injected lifecycle warm seam.
type WarmOperation func(context.Context, WarmRequest) (lifecycle.WarmResult, error)

// WarmInput contains the raw public options. Count stays a string until it is
// validated so malformed input cannot reach lifecycle mutation.
type WarmInput struct {
	Count string
	From  string
	Fetch bool
	JSON  bool
}

// WarmResult includes the stable lifecycle result and selected rendering.
type WarmResult struct {
	lifecycle.WarmResult
	Output string
}

// Warm validates count, invokes the injected operation, validates its stable
// result, and renders the TypeScript-compatible output.
func Warm(ctx context.Context, input WarmInput, operation WarmOperation) (WarmResult, error) {
	count, err := parsePositiveSafeInt(input.Count, 0, "--count")
	if err != nil {
		return WarmResult{}, err
	}
	if operation == nil {
		return WarmResult{}, errors.New("warm operation is not configured")
	}
	result, err := operation(ctx, WarmRequest{Count: count, From: input.From, Fetch: input.Fetch})
	if err != nil {
		return WarmResult{}, err
	}
	if err := validateWarmResult(result, count); err != nil {
		return WarmResult{}, err
	}
	if result.Created == nil {
		result.Created = []string{}
	}
	var output string
	if input.JSON {
		encoded, err := json.Marshal(result)
		if err != nil {
			return WarmResult{}, fmt.Errorf("encode warm result: %w", err)
		}
		output = string(encoded) + "\n"
	} else {
		output = fmt.Sprintf("Available workspaces: %d (%d created)\n", result.Available, len(result.Created))
	}
	return WarmResult{WarmResult: result, Output: output}, nil
}

// GCRequest is the validated command handoff to the injected GC operation.
type GCRequest struct {
	Options lifecycle.GCOptions
}

// GCOperation is the injected lifecycle GC seam.
type GCOperation func(context.Context, GCRequest) (lifecycle.GCResult, error)

// GCInput contains raw public GC options and the clock used to compute its
// cutoff. A zero Now uses the current wall clock at validation time.
type GCInput struct {
	MaxAgeMinutes        string
	Apply                bool
	ForceExpired         bool
	JSON                 bool
	CurrentWorkspacePath string
	Now                  time.Time
}

// GCResult includes the stable lifecycle result and selected rendering.
type GCResult struct {
	lifecycle.GCResult
	Output string
}

// GC validates max age and force policy before invoking the injected
// operation, then renders the stable plan/apply output.
func GC(ctx context.Context, input GCInput, operation GCOperation) (GCResult, error) {
	if input.ForceExpired && !input.Apply {
		return GCResult{}, errors.New("--force-expired requires --apply")
	}
	minutes, err := parseNonNegativeFiniteMinutes(input.MaxAgeMinutes, defaultGCMaxAgeMinutes)
	if err != nil {
		return GCResult{}, err
	}
	if operation == nil {
		return GCResult{}, errors.New("gc operation is not configured")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	duration, err := minutesDuration(minutes)
	if err != nil {
		return GCResult{}, err
	}
	cutoff := now.Add(-duration)
	request := GCRequest{Options: lifecycle.GCOptions{
		OlderThan: cutoff, Now: now, Apply: input.Apply, ForceExpired: input.ForceExpired, CurrentWorkspacePath: input.CurrentWorkspacePath,
	}}
	result, err := operation(ctx, request)
	if err != nil {
		return GCResult{}, err
	}
	if err := validateGCResult(result, input.Apply); err != nil {
		return GCResult{}, err
	}
	if result.Removed == nil {
		result.Removed = []lifecycle.GCRemovedRecord{}
	}
	if result.Expired == nil {
		result.Expired = []lifecycle.GCExpiredRecord{}
	}
	var output string
	if input.JSON {
		encoded, err := json.Marshal(result)
		if err != nil {
			return GCResult{}, fmt.Errorf("encode gc result: %w", err)
		}
		output = string(encoded) + "\n"
	} else {
		verb := "Would collect"
		if input.Apply {
			verb = "Collected"
		}
		output = fmt.Sprintf("%s: %d workspace(s)\n", verb, len(result.Removed))
		if len(result.Expired) > 0 {
			output += fmt.Sprintf("Expired assignments: %d\n", len(result.Expired))
		}
	}
	return GCResult{GCResult: result, Output: output}, nil
}

func parsePositiveSafeInt(value string, fallback int, name string) (int, error) {
	if value == "" {
		if fallback == 0 {
			return 0, fmt.Errorf("%s must be a positive integer", name)
		}
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 1 || math.Trunc(parsed) != parsed || parsed > float64(maxSafeInt) {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return int(parsed), nil
}

func parseNonNegativeFiniteMinutes(value string, fallback float64) (float64, error) {
	minutes := fallback
	if value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, errors.New("--max-age must be a non-negative number of minutes")
		}
		minutes = parsed
	}
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes < 0 {
		return 0, errors.New("--max-age must be a non-negative number of minutes")
	}
	return minutes, nil
}

func minutesDuration(minutes float64) (time.Duration, error) {
	nanoseconds := minutes * float64(time.Minute)
	// float64(math.MaxInt64) rounds up to 2^63, which would wrap when
	// converted to time.Duration. Reject the rounded boundary as well.
	if math.IsInf(nanoseconds, 0) || nanoseconds >= float64(math.MaxInt64) {
		return 0, errors.New("--max-age must be a non-negative number of minutes")
	}
	return time.Duration(nanoseconds), nil
}

func validateWarmResult(result lifecycle.WarmResult, requested int) error {
	if result.Status != "warmed" {
		return fmt.Errorf("warm operation returned status %q, expected warmed", result.Status)
	}
	if result.Requested != requested {
		return fmt.Errorf("warm operation returned requested count %d, expected %d", result.Requested, requested)
	}
	if result.Available < requested || result.Available < 0 {
		return errors.New("warm operation returned an invalid available count")
	}
	if result.Created != nil && len(result.Created) > result.Available {
		return errors.New("warm operation returned more created workspaces than available")
	}
	return nil
}

func validateGCResult(result lifecycle.GCResult, apply bool) error {
	wantStatus := "planned"
	if apply {
		wantStatus = "collected"
	}
	if result.Status != wantStatus {
		return fmt.Errorf("gc operation returned status %q, expected %s", result.Status, wantStatus)
	}
	for _, removed := range result.Removed {
		if strings.TrimSpace(removed.Path) == "" || strings.TrimSpace(removed.Lifecycle) == "" || strings.TrimSpace(removed.Reason) == "" {
			return errors.New("gc operation returned a malformed removed record")
		}
	}
	for _, expired := range result.Expired {
		if strings.TrimSpace(expired.Path) == "" || strings.TrimSpace(expired.AssignmentID) == "" || strings.TrimSpace(expired.ExpiresAt) == "" {
			return errors.New("gc operation returned a malformed expired record")
		}
	}
	return nil
}
