package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

// AcquireOperation is the orchestration seam used by the acquire command.
// Repository/Git/dependency setup belongs behind this operation; the CLI only
// validates its public inputs and renders the resulting lifecycle record.
type AcquireOperation func(context.Context, AcquireOperationInput) (lifecycle.AcquisitionResult, error)

// AcquireOperationInput is the structured acquisition request passed to the
// injected operation after TTL and owner validation.
type AcquireOperationInput struct {
	Branch string
	From   string
	// StartPoint mirrors From for lifecycle adapters that use Git terminology.
	StartPoint string
	Fetch      bool
	Owner      string
	Hostname   string
	ExpiresAt  time.Time
	Ports      []string
	JSON       bool
}

// AcquireInput is the parsed command input and deterministic rendering
// context. OwnerFallback and HostnameFallback are injectable for tests and
// embedding applications.
type AcquireInput struct {
	Branch           string
	From             string
	Fetch            bool
	TTL              string
	Owner            string
	Ports            []string
	JSON             bool
	Now              time.Time
	OwnerFallback    func() string
	Hostname         string
	HostnameFallback func() string
}

// AcquireRecord is the stable machine-readable acquire success contract.
type AcquireRecord struct {
	Status       string           `json:"status"`
	AssignmentID string           `json:"assignmentId"`
	Path         string           `json:"path"`
	Branch       string           `json:"branch"`
	ExpiresAt    string           `json:"expiresAt"`
	Reused       bool             `json:"reused"`
	Fingerprint  string           `json:"fingerprint"`
	Mode         string           `json:"mode"`
	Ports        map[string]int64 `json:"ports"`
}

// AcquireResult includes the public record and its selected rendering.
type AcquireResult struct {
	AcquireRecord
	Output string
}

// Acquire validates the command contract before calling operation and emits
// the documented JSON or human success response. Invalid TTLs therefore
// cannot trigger acquisition-side mutation.
func Acquire(ctx context.Context, input AcquireInput, operation AcquireOperation) (AcquireResult, error) {
	if input.Branch == "" {
		return AcquireResult{}, errors.New("branch must not be empty")
	}
	if operation == nil {
		return AcquireResult{}, errors.New("acquire operation is not configured")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	expiresAt, err := ParseFutureMinutes(input.TTL, defaultLeaseMinutes, "--ttl", now)
	if err != nil {
		return AcquireResult{}, err
	}
	owner := input.Owner
	if owner == "" {
		fallback := input.OwnerFallback
		if fallback == nil {
			fallback = defaultOwnerFallback
		}
		owner = fallback()
	}
	if owner == "" {
		return AcquireResult{}, errors.New("owner must not be empty")
	}
	hostname := input.Hostname
	if hostname == "" {
		fallback := input.HostnameFallback
		if fallback == nil {
			fallback = defaultHostnameFallback
		}
		hostname = fallback()
	}
	if hostname == "" {
		return AcquireResult{}, errors.New("hostname must not be empty")
	}
	operationInput := AcquireOperationInput{
		Branch:     input.Branch,
		From:       input.From,
		StartPoint: input.From,
		Fetch:      input.Fetch,
		Owner:      owner,
		Hostname:   hostname,
		ExpiresAt:  expiresAt,
		Ports:      append([]string(nil), input.Ports...),
		JSON:       input.JSON,
	}
	acquisition, err := operation(ctx, operationInput)
	if err != nil {
		return AcquireResult{}, err
	}
	if acquisition.Workspace.Assignment == nil {
		return AcquireResult{}, errors.New("acquire operation returned a workspace without an assignment")
	}
	if acquisition.Workspace.Lifecycle != state.LifecycleAssigned {
		return AcquireResult{}, fmt.Errorf("acquire operation returned workspace in %s state, expected assigned", acquisition.Workspace.Lifecycle)
	}
	if acquisition.Workspace.OperationID != nil {
		return AcquireResult{}, errors.New("acquire operation returned while its operation is still in progress")
	}
	assignment := acquisition.Workspace.Assignment
	path := acquisition.Path
	if path == "" {
		path = acquisition.Workspace.Path
	}
	branch := acquisition.Branch
	if branch == "" {
		branch = acquisition.Workspace.Branch
	}
	if assignment.ID == "" || path == "" || branch == "" || assignment.ExpiresAt == "" {
		return AcquireResult{}, errors.New("acquire operation returned an incomplete workspace assignment")
	}
	ports := assignment.Ports
	if ports == nil {
		ports = map[string]int64{}
	}
	record := AcquireRecord{
		Status:       "assigned",
		AssignmentID: assignment.ID,
		Path:         path,
		Branch:       branch,
		ExpiresAt:    assignment.ExpiresAt,
		Reused:       acquisition.Reused,
		Fingerprint:  acquisition.Fingerprint,
		Mode:         acquisition.Mode,
		Ports:        ports,
	}
	output, err := formatAcquireOutput(record, input.JSON)
	if err != nil {
		return AcquireResult{}, err
	}
	return AcquireResult{AcquireRecord: record, Output: output}, nil
}

func formatAcquireOutput(record AcquireRecord, jsonMode bool) (string, error) {
	if jsonMode {
		encoded, err := json.Marshal(record)
		if err != nil {
			return "", fmt.Errorf("encode acquire result: %w", err)
		}
		return string(encoded) + "\n", nil
	}
	output := fmt.Sprintf("Assigned %s\nAssignment: %s\nExpires: %s\n", record.Path, record.AssignmentID, record.ExpiresAt)
	keys := make([]string, 0, len(record.Ports))
	for name := range record.Ports {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		output += name + ": " + strconv.FormatInt(record.Ports[name], 10) + "\n"
	}
	return output, nil
}

func defaultOwnerFallback() string {
	if owner := os.Getenv("RUK_AGENT_ID"); owner != "" {
		return owner
	}
	hostname := defaultHostnameFallback()
	if hostname == "" {
		return ""
	}
	return hostname + ":" + strconv.Itoa(os.Getpid())
}

func defaultHostnameFallback() string {
	hostname, _ := os.Hostname()
	return hostname
}
