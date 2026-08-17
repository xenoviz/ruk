package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

const defaultLeaseMinutes = 480

// RenewOperation is the lifecycle seam used by the renew command.
type RenewOperation func(context.Context, string, time.Time) (state.WorkspaceRecord, error)

// RenewInput is the validated syntactic input plus its deterministic clock.
type RenewInput struct {
	AssignmentID string
	TTL          string
	JSON         bool
	Now          time.Time
}

// RenewRecord is the stable machine-readable renew result.
type RenewRecord struct {
	Status       string `json:"status"`
	AssignmentID string `json:"assignmentId"`
	Path         string `json:"path"`
	ExpiresAt    string `json:"expiresAt"`
}

// RenewResult includes the result record and its selected public rendering.
type RenewResult struct {
	RenewRecord
	Output string
}

// FutureExpiry converts a finite positive duration into a millisecond lease
// without overflowing time.Time or emitting a year outside RFC 3339.
func FutureExpiry(now time.Time, minutes float64) (time.Time, error) {
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 {
		return time.Time{}, errors.New("duration must be finite and positive")
	}
	now = now.UTC()
	maximum := time.Date(9999, 12, 31, 23, 59, 59, 999_000_000, time.UTC)
	seconds := minutes * 60
	availableSeconds := float64(maximum.Unix()-now.Unix()) + float64(maximum.Nanosecond()-now.Nanosecond())/1e9
	if math.IsInf(seconds, 0) || seconds > availableSeconds {
		return time.Time{}, errors.New("duration exceeds supported timestamp range")
	}
	whole, fraction := math.Modf(seconds)
	expiresAt := time.Unix(now.Unix()+int64(whole), int64(now.Nanosecond())+int64(math.Round(fraction*1e9))).UTC().Truncate(time.Millisecond)
	if !expiresAt.After(now) {
		return time.Time{}, errors.New("duration does not produce a future millisecond")
	}
	return expiresAt, nil
}

// ParseFutureMinutes applies the CLI's positive-minutes contract and validates
// the resulting timestamp before any lifecycle mutation occurs.
func ParseFutureMinutes(value string, fallback float64, name string, now time.Time) (time.Time, error) {
	minutes, err := ParseLeaseMinutes(value, fallback, name)
	if err != nil {
		return time.Time{}, err
	}
	expiresAt, err := FutureExpiry(now, minutes)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be a positive number of minutes", name)
	}
	return expiresAt, nil
}

// ParseLeaseMinutes validates and returns the requested lease duration. The
// duration is carried separately from the initial expiry so lifecycle can
// restart the lease clock after acquisition preparation has completed.
func ParseLeaseMinutes(value string, fallback float64, name string) (float64, error) {
	minutes := fallback
	if value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be a positive number of minutes", name)
		}
		minutes = parsed
	}
	if math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 {
		return 0, fmt.Errorf("%s must be a positive number of minutes", name)
	}
	return minutes, nil
}

// Renew validates expiry before delegating to lifecycle and formats one stable
// human or machine-readable success record.
func Renew(ctx context.Context, input RenewInput, operation RenewOperation) (RenewResult, error) {
	if input.AssignmentID == "" {
		return RenewResult{}, errors.New("assignment ID must not be empty")
	}
	if operation == nil {
		return RenewResult{}, errors.New("renew operation is not configured")
	}
	expiresAt, err := ParseFutureMinutes(input.TTL, defaultLeaseMinutes, "--ttl", input.Now)
	if err != nil {
		return RenewResult{}, err
	}
	workspace, err := operation(ctx, input.AssignmentID, expiresAt)
	if err != nil {
		return RenewResult{}, err
	}
	if workspace.Assignment == nil {
		return RenewResult{}, errors.New("renew operation returned a workspace without an assignment")
	}
	record := RenewRecord{
		Status:       "renewed",
		AssignmentID: workspace.Assignment.ID,
		Path:         workspace.Path,
		ExpiresAt:    workspace.Assignment.ExpiresAt,
	}
	var output string
	if input.JSON {
		encoded, err := json.Marshal(record)
		if err != nil {
			return RenewResult{}, fmt.Errorf("encode renew result: %w", err)
		}
		output = string(encoded) + "\n"
	} else {
		output = fmt.Sprintf("Renewed %s until %s\n", record.AssignmentID, record.ExpiresAt)
	}
	return RenewResult{RenewRecord: record, Output: output}, nil
}
