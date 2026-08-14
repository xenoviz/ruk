package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxSafeInteger int64 = 9_007_199_254_740_991

var (
	projectionFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuidVersionFourPattern       = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type rawState struct {
	Version    int                        `json:"version"`
	Trees      map[string]json.RawMessage `json:"trees"`
	Workspaces map[string]rawWorkspace    `json:"workspaces"`
	Metrics    *UsageMetrics              `json:"metrics"`
}

type rawWorkspace struct {
	Path        string                 `json:"path"`
	Managed     bool                   `json:"managed"`
	Branch      string                 `json:"branch"`
	Lifecycle   WorkspaceLifecycle     `json:"lifecycle"`
	OperationID *string                `json:"operationId"`
	Assignment  *rawAssignment         `json:"assignment"`
	Processes   []TrackedProcessRecord `json:"processes"`
	CreatedAt   string                 `json:"createdAt"`
	UpdatedAt   string                 `json:"updatedAt"`
	AvailableAt *string                `json:"availableAt"`
	Failure     *string                `json:"failure"`
}

type rawAssignment struct {
	ID                   string               `json:"id"`
	Owner                string               `json:"owner"`
	Hostname             string               `json:"hostname"`
	AssignedAt           string               `json:"assignedAt"`
	RenewedAt            string               `json:"renewedAt"`
	ExpiresAt            string               `json:"expiresAt"`
	LeaseDurationMinutes *float64             `json:"leaseDurationMinutes"`
	LastActivityAt       *string              `json:"lastActivityAt"`
	LeaseKeepers         *[]LeaseKeeperRecord `json:"leaseKeepers"`
	Ports                map[string]int64     `json:"ports"`
}

// Decode parses, migrates, and validates one persisted Ruk state document.
func Decode(data []byte, source string) (*State, error) {
	var persisted rawState
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("Cannot parse Ruk state in %s: %w", source, err)
	}
	trees, validTrees := decodeTrees(persisted.Trees)
	if !validTrees {
		return nil, invalidState(source)
	}
	if persisted.Version == 1 {
		return &State{
			Version:    CurrentVersion,
			Trees:      trees,
			Workspaces: map[string]WorkspaceRecord{},
			Metrics:    EmptyMetrics(),
		}, nil
	}
	if persisted.Version < 2 || persisted.Version > CurrentVersion || persisted.Workspaces == nil {
		return nil, invalidState(source)
	}

	metrics := EmptyMetrics()
	if persisted.Version != 2 {
		if persisted.Metrics == nil || !validMetrics(*persisted.Metrics) {
			return nil, invalidState(source)
		}
		metrics = *persisted.Metrics
	}

	workspaces := make(map[string]WorkspaceRecord, len(persisted.Workspaces))
	for key, raw := range persisted.Workspaces {
		workspace, err := migrateWorkspace(raw, persisted.Version)
		if err != nil {
			return nil, invalidState(source)
		}
		derivedKey, err := TreeKey(workspace.Path)
		if err != nil || key != derivedKey || !validWorkspace(workspace) {
			return nil, invalidState(source)
		}
		workspaces[key] = workspace
	}

	decoded := &State{
		Version:    CurrentVersion,
		Trees:      trees,
		Workspaces: workspaces,
		Metrics:    metrics,
	}
	if !validGlobalFences(decoded) {
		return nil, invalidState(source)
	}
	return decoded, nil
}

// TreeKey returns the state key for an absolute, platform-native worktree path.
func TreeKey(treePath string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(treePath))
	if err != nil {
		return "", fmt.Errorf("resolve tree path: %w", err)
	}
	digest := sha256.Sum256([]byte(absolute))
	return hex.EncodeToString(digest[:])[:20], nil
}

func migrateWorkspace(raw rawWorkspace, version int) (WorkspaceRecord, error) {
	workspace := WorkspaceRecord{
		Path:        raw.Path,
		Managed:     raw.Managed,
		Branch:      raw.Branch,
		Lifecycle:   raw.Lifecycle,
		OperationID: raw.OperationID,
		Processes:   raw.Processes,
		CreatedAt:   raw.CreatedAt,
		UpdatedAt:   raw.UpdatedAt,
		AvailableAt: raw.AvailableAt,
		Failure:     raw.Failure,
	}
	if raw.Assignment == nil {
		return workspace, nil
	}

	ports := raw.Assignment.Ports
	if version == 2 {
		ports = map[string]int64{}
	}
	if ports == nil {
		return WorkspaceRecord{}, fmt.Errorf("assignment ports are missing")
	}

	leaseDuration := raw.Assignment.LeaseDurationMinutes
	lastActivity := raw.Assignment.LastActivityAt
	leaseKeepers := raw.Assignment.LeaseKeepers
	if version != CurrentVersion {
		renewedAt, renewedOK := canonicalTimestamp(raw.Assignment.RenewedAt)
		expiresAt, expiresOK := canonicalTimestamp(raw.Assignment.ExpiresAt)
		if !renewedOK || !expiresOK {
			return WorkspaceRecord{}, fmt.Errorf("assignment timestamps are invalid")
		}
		minutes := expiresAt.Sub(renewedAt).Minutes()
		leaseDuration = &minutes
		activity := raw.Assignment.RenewedAt
		lastActivity = &activity
		emptyKeepers := []LeaseKeeperRecord{}
		leaseKeepers = &emptyKeepers
	}
	if leaseDuration == nil || lastActivity == nil || leaseKeepers == nil {
		return WorkspaceRecord{}, fmt.Errorf("assignment activity fields are missing")
	}

	workspace.Assignment = &AssignmentRecord{
		ID:                   raw.Assignment.ID,
		Owner:                raw.Assignment.Owner,
		Hostname:             raw.Assignment.Hostname,
		AssignedAt:           raw.Assignment.AssignedAt,
		RenewedAt:            raw.Assignment.RenewedAt,
		ExpiresAt:            raw.Assignment.ExpiresAt,
		LeaseDurationMinutes: *leaseDuration,
		LastActivityAt:       *lastActivity,
		LeaseKeepers:         *leaseKeepers,
		Ports:                ports,
	}
	return workspace, nil
}

func decodeTrees(rawTrees map[string]json.RawMessage) (map[string]TreeRecord, bool) {
	if rawTrees == nil {
		return nil, false
	}
	trees := make(map[string]TreeRecord, len(rawTrees))
	for key, rawTree := range rawTrees {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawTree, &fields); err != nil || fields == nil {
			return nil, false
		}
		path, pathOK := requiredString(fields, "path")
		fingerprint, fingerprintOK := requiredString(fields, "fingerprint")
		mode, modeOK := requiredString(fields, "mode")
		branch, branchOK := requiredString(fields, "branch")
		updatedAt, updatedOK := requiredString(fields, "updatedAt")
		projectionsRaw, projectionsOK := fields["projections"]
		var projections []string
		if projectionsOK {
			projectionsOK = json.Unmarshal(projectionsRaw, &projections) == nil && projections != nil
		}
		if !pathOK || !fingerprintOK || !modeOK || !branchOK || !updatedOK || !projectionsOK {
			return nil, false
		}

		projectionFingerprint := ""
		if rawFingerprint, exists := fields["projectionFingerprint"]; exists {
			var fingerprintOK bool
			projectionFingerprint, fingerprintOK = jsonString(rawFingerprint)
			if !fingerprintOK || !projectionFingerprintPattern.MatchString(projectionFingerprint) {
				return nil, false
			}
		}
		trees[key] = TreeRecord{
			Path:                  path,
			Fingerprint:           fingerprint,
			ProjectionFingerprint: projectionFingerprint,
			Mode:                  mode,
			Projections:           projections,
			Branch:                branch,
			UpdatedAt:             updatedAt,
		}
	}
	return trees, true
}

func requiredString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, exists := fields[name]
	if !exists {
		return "", false
	}
	return jsonString(raw)
}

func jsonString(raw json.RawMessage) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	text, valid := value.(string)
	return text, valid
}

func validMetrics(metrics UsageMetrics) bool {
	counters := []int64{
		metrics.Acquisitions,
		metrics.WorkspaceReuses,
		metrics.Preparations,
		metrics.PreparationSkips,
		metrics.PreparationFailures,
		metrics.TotalPreparationMS,
	}
	for _, counter := range counters {
		if counter < 0 || counter > maxSafeInteger {
			return false
		}
	}
	return metrics.LastPreparationMS == nil || (*metrics.LastPreparationMS >= 0 && *metrics.LastPreparationMS <= maxSafeInteger)
}

func validWorkspace(workspace WorkspaceRecord) bool {
	if !filepath.IsAbs(workspace.Path) || !workspace.Managed || workspace.Branch == "" || workspace.Processes == nil {
		return false
	}
	if !validLifecycle(workspace.Lifecycle) || !validTimestamp(workspace.CreatedAt) || !validTimestamp(workspace.UpdatedAt) {
		return false
	}
	if workspace.OperationID != nil && !validUUID(*workspace.OperationID) {
		return false
	}
	if workspace.Lifecycle == LifecyclePreparing && workspace.OperationID == nil {
		return false
	}
	assigned := workspace.Lifecycle == LifecycleAssigned || workspace.Lifecycle == LifecycleReturning
	if assigned != (workspace.Assignment != nil) {
		return false
	}
	if assigned && !validAssignment(*workspace.Assignment) {
		return false
	}
	if workspace.Lifecycle == LifecycleAvailable {
		if workspace.AvailableAt == nil || !validTimestamp(*workspace.AvailableAt) {
			return false
		}
	} else if workspace.AvailableAt != nil {
		return false
	}
	if workspace.Lifecycle == LifecycleFailed {
		if workspace.Failure == nil || *workspace.Failure == "" {
			return false
		}
	} else if workspace.Lifecycle == LifecycleAssigned {
		if workspace.Failure != nil && *workspace.Failure == "" {
			return false
		}
	} else if workspace.Failure != nil {
		return false
	}
	if !assigned && len(workspace.Processes) != 0 {
		return false
	}
	return validProcesses(workspace.Processes)
}

func validLifecycle(lifecycle WorkspaceLifecycle) bool {
	switch lifecycle {
	case LifecycleAvailable, LifecyclePreparing, LifecycleAssigned, LifecycleReturning, LifecycleFailed:
		return true
	default:
		return false
	}
}

func validAssignment(assignment AssignmentRecord) bool {
	assignedAt, assignedOK := canonicalTimestamp(assignment.AssignedAt)
	renewedAt, renewedOK := canonicalTimestamp(assignment.RenewedAt)
	expiresAt, expiresOK := canonicalTimestamp(assignment.ExpiresAt)
	lastActivityAt, activityOK := canonicalTimestamp(assignment.LastActivityAt)
	if !validUUID(assignment.ID) || assignment.Owner == "" || assignment.Hostname == "" || !assignedOK || !renewedOK || !expiresOK || !activityOK {
		return false
	}
	if assignedAt.After(renewedAt) || !renewedAt.Before(expiresAt) || assignedAt.After(lastActivityAt) || !lastActivityAt.Before(expiresAt) {
		return false
	}
	if math.IsNaN(assignment.LeaseDurationMinutes) || math.IsInf(assignment.LeaseDurationMinutes, 0) || assignment.LeaseDurationMinutes <= 0 {
		return false
	}
	if assignment.LeaseKeepers == nil || assignment.Ports == nil {
		return false
	}
	keeperIDs := map[string]struct{}{}
	for _, keeper := range assignment.LeaseKeepers {
		heartbeatAt, heartbeatOK := canonicalTimestamp(keeper.HeartbeatAt)
		validUntil, validUntilOK := canonicalTimestamp(keeper.ValidUntil)
		if !validUUID(keeper.ID) || !heartbeatOK || !validUntilOK || !heartbeatAt.Before(validUntil) {
			return false
		}
		if _, exists := keeperIDs[keeper.ID]; exists {
			return false
		}
		keeperIDs[keeper.ID] = struct{}{}
	}
	for name, port := range assignment.Ports {
		if name == "" || port < 1 || port > 65_535 {
			return false
		}
	}
	return true
}

func validProcesses(processes []TrackedProcessRecord) bool {
	seen := map[int64]struct{}{}
	for _, process := range processes {
		if !validSafePositive(process.PID) || process.StartedAt == "" {
			return false
		}
		if _, exists := seen[process.PID]; exists {
			return false
		}
		seen[process.PID] = struct{}{}
		if process.GroupID != nil && !validSafePositive(*process.GroupID) {
			return false
		}
		if process.SessionID != nil && (!validSafePositive(*process.SessionID) || process.SessionStartedAt == nil || *process.SessionStartedAt == "") {
			return false
		}
		if process.SessionStartedAt != nil && *process.SessionStartedAt == "" {
			return false
		}
		if process.TerminalID != nil && (*process.TerminalID == "" || *process.TerminalID == "??") {
			return false
		}
		if process.Command != nil && len(process.Command) == 0 {
			return false
		}
	}
	return true
}

func validGlobalFences(state *State) bool {
	assignments := map[string]struct{}{}
	operations := map[string]struct{}{}
	ports := map[int64]struct{}{}
	for _, workspace := range state.Workspaces {
		if workspace.OperationID != nil {
			if _, exists := operations[*workspace.OperationID]; exists {
				return false
			}
			operations[*workspace.OperationID] = struct{}{}
		}
		if workspace.Assignment == nil {
			continue
		}
		if _, exists := assignments[workspace.Assignment.ID]; exists {
			return false
		}
		assignments[workspace.Assignment.ID] = struct{}{}
		for _, port := range workspace.Assignment.Ports {
			if _, exists := ports[port]; exists {
				return false
			}
			ports[port] = struct{}{}
		}
	}
	return true
}

func canonicalTimestamp(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, parsed.UTC().Format("2006-01-02T15:04:05.000Z") == value
}

func validTimestamp(value string) bool {
	_, valid := canonicalTimestamp(value)
	return valid
}

func validUUID(value string) bool {
	return uuidVersionFourPattern.MatchString(value)
}

func validSafePositive(value int64) bool {
	return value > 0 && value <= maxSafeInteger
}

func invalidState(source string) error {
	return fmt.Errorf("Unsupported or invalid Ruk state in %s", strings.TrimSpace(source))
}
