package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
)

// ErrorCode is the stable machine-readable category emitted for a failed
// command. Consumers must ignore codes they do not know about.
type ErrorCode string

const (
	InvalidArgumentCode       ErrorCode = "INVALID_ARGUMENT"
	AssignmentConflictCode    ErrorCode = "ASSIGNMENT_CONFLICT"
	WorkspaceDirtyCode        ErrorCode = "WORKSPACE_DIRTY"
	PortUnavailableCode       ErrorCode = "PORT_UNAVAILABLE"
	ResourceBusyCode          ErrorCode = "RESOURCE_BUSY"
	DependencyPreparationCode ErrorCode = "DEPENDENCY_PREPARATION_FAILED"
	GitOperationFailedCode    ErrorCode = "GIT_OPERATION_FAILED"
	OperationFailedCode       ErrorCode = "OPERATION_FAILED"
)

// ErrorCategory is retained as an alias for callers that describe these
// values as categories rather than codes.
type ErrorCategory = ErrorCode

// ErrorRecord is the JSON failure contract documented by agent-interface.md.
// Optional recovery fields are omitted unless the error supplies them.
type ErrorRecord struct {
	Status            string    `json:"status"`
	Code              ErrorCode `json:"code"`
	Message           string    `json:"message"`
	Retryable         bool      `json:"retryable"`
	ActiveAssignments int       `json:"activeAssignments,omitempty"`
	AssignmentID      string    `json:"assignmentId,omitempty"`
	Path              string    `json:"path,omitempty"`
	ExpiresAt         string    `json:"expiresAt,omitempty"`
	Recovery          string    `json:"recovery,omitempty"`
}

// SharedCheckoutError is returned when a task command is attempted from the
// primary checkout while assignments are active.
type SharedCheckoutError struct {
	ActiveAssignments int
	Recovery          string
}

// SharedCheckoutWarning reports active assignments under the explicit warn
// policy. It is handled as a diagnostic by guarded sync and never reaches
// stdout or JSON output.
type SharedCheckoutWarning struct{ ActiveAssignments int }

func (warning *SharedCheckoutWarning) Error() string {
	if warning == nil {
		return "Primary checkout has active Ruk assignments; continuing because sharedCheckoutPolicy is warn"
	}
	return fmt.Sprintf("Primary checkout has %d active Ruk assignment(s); continuing because sharedCheckoutPolicy is warn", warning.ActiveAssignments)
}

func (err *SharedCheckoutError) Error() string {
	if err == nil {
		return "Primary checkout is shared by active Ruk assignments"
	}
	plural := "assignments"
	if err.ActiveAssignments == 1 {
		plural = "assignment"
	}
	return fmt.Sprintf(
		"Primary checkout has %d active Ruk %s; acquire a dedicated workspace or pass --allow-shared-checkout",
		err.ActiveAssignments,
		plural,
	)
}

// AssignmentActivityError preserves the stable activity-renewal message and
// lets errors.As/errors.Is inspect the underlying state or lock failure.
type AssignmentActivityError struct {
	AssignmentID string
	Cause        error
}

func (err *AssignmentActivityError) Error() string {
	if err == nil {
		return "assignment activity renewal failed"
	}
	return fmt.Sprintf("Assignment %s activity renewal failed: %s", err.AssignmentID, errorDetail(err.Cause))
}

func (err *AssignmentActivityError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// RetainedAssignmentError carries the fenced recovery data needed when Ruk
// cannot safely release an assignment.
type RetainedAssignmentError struct {
	AssignmentID string
	Path         string
	ExpiresAt    string
	Cause        error
}

func (err *RetainedAssignmentError) Error() string {
	if err == nil {
		return "assignment retained"
	}
	return fmt.Sprintf("Assignment %s retained at %s: %s", err.AssignmentID, err.Path, errorDetail(err.Cause))
}

func (err *RetainedAssignmentError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// NewSharedCheckoutError uses the documented recovery command by default.
func NewSharedCheckoutError(activeAssignments int) *SharedCheckoutError {
	return &SharedCheckoutError{
		ActiveAssignments: activeAssignments,
		Recovery:          "ruk acquire <branch>",
	}
}

// NewAssignmentActivityError constructs the stable activity error wrapper.
func NewAssignmentActivityError(assignmentID string, cause error) *AssignmentActivityError {
	return &AssignmentActivityError{AssignmentID: assignmentID, Cause: cause}
}

// NewRetainedAssignmentError constructs an error with fenced recovery data.
func NewRetainedAssignmentError(assignmentID, path, expiresAt string, cause error) *RetainedAssignmentError {
	return &RetainedAssignmentError{AssignmentID: assignmentID, Path: path, ExpiresAt: expiresAt, Cause: cause}
}

// RetainedAssignmentFailure wraps an error only when process safety cannot be
// proved. This mirrors the TypeScript retainedAssignmentFailure helper.
func RetainedAssignmentFailure(assignmentID, path, expiresAt string, err error) *RetainedAssignmentError {
	if !ContainsProcessSafetyError(err) {
		return nil
	}
	return NewRetainedAssignmentError(assignmentID, path, expiresAt, err)
}

// ContainsProcessSafetyError reports whether an error chain contains the
// typed process identity failure used by lifecycle cleanup.
func ContainsProcessSafetyError(err error) bool {
	if err == nil {
		return false
	}
	var unavailable *processpkg.IdentityUnavailableError
	if errors.As(err, &unavailable) {
		return true
	}
	var unsafeCleanup *processpkg.ProcessCleanupUnsafeError
	if errors.As(err, &unsafeCleanup) {
		return true
	}
	var registration *processpkg.RegistrationError
	if errors.As(err, &registration) && registration.Cleanup != nil {
		return true
	}
	var setup *processpkg.ProcessSetupError
	return errors.As(err, &setup) && setup.Cleanup != nil
}

// ClassifyError maps an error to the stable public failure record.
func ClassifyError(err error) ErrorRecord {
	return classifyError(err, true)
}

// classifyError keeps lifecycle recovery metadata separate from the ordinary
// category walk. A retained acquisition can still have a stable dependency
// or port cause; inspecting that cause without re-entering the lifecycle
// wrapper avoids turning every retained failure into RESOURCE_BUSY.
func classifyError(err error, inspectLifecycle bool) ErrorRecord {
	record := ErrorRecord{Status: "error", Code: OperationFailedCode, Message: errorDetail(err)}

	var lifecycleRetained *lifecycle.RetainedAssignmentError
	if inspectLifecycle && errors.As(err, &lifecycleRetained) && lifecycleRetained != nil {
		causeRecord := classifyError(lifecycleRetained.Cause, false)
		if causeRecord.Code == OperationFailedCode && !causeRecord.Retryable {
			// With no stable underlying category, the fenced recovery state is
			// itself the actionable resource-busy condition.
			causeRecord.Code = ResourceBusyCode
			causeRecord.Retryable = true
		}
		record.Code = causeRecord.Code
		record.Retryable = causeRecord.Retryable
		record.AssignmentID = lifecycleRetained.AssignmentID
		record.Path = lifecycleRetained.Path
		record.ExpiresAt = lifecycleRetained.ExpiresAt
		record.Recovery = lifecycleRetained.Recovery
		if record.Recovery == "" && record.AssignmentID != "" {
			record.Recovery = "ruk release " + record.AssignmentID
		}
		return record
	}

	var retained *RetainedAssignmentError
	if errors.As(err, &retained) && retained != nil {
		record.Code = ResourceBusyCode
		record.Retryable = true
		record.AssignmentID = retained.AssignmentID
		record.Path = retained.Path
		record.ExpiresAt = retained.ExpiresAt
		record.Recovery = "ruk release " + retained.AssignmentID
		return record
	}

	var shared *SharedCheckoutError
	if errors.As(err, &shared) && shared != nil {
		record.Code = ResourceBusyCode
		record.Retryable = true
		record.ActiveAssignments = shared.ActiveAssignments
		record.Recovery = shared.Recovery
		if record.Recovery == "" {
			record.Recovery = "ruk acquire <branch>"
		}
		return record
	}

	var activity *AssignmentActivityError
	if errors.As(err, &activity) && activity != nil {
		record.Code = ResourceBusyCode
		record.Retryable = true
		return record
	}

	// DependencyPreparationError deliberately comes before process safety in
	// this branch, matching errors.ts for a typed dependency wrapper. Activity
	// errors above still win when an errors.Join contains both failures.
	var preparation *dependencies.DependencyPreparationError
	if errors.As(err, &preparation) && preparation != nil {
		record.Code = DependencyPreparationCode
		record.Retryable = true
		return record
	}

	var unavailable *processpkg.IdentityUnavailableError
	if errors.As(err, &unavailable) && unavailable != nil {
		record.Code = ResourceBusyCode
		record.Retryable = true
		return record
	}

	var timeout *lock.TimeoutError
	if errors.As(err, &timeout) && timeout != nil {
		record.Code = ResourceBusyCode
		record.Retryable = true
		return record
	}

	record.Code, record.Retryable = classifyMessage(record.Message)
	return record
}

// errorRecord is kept private for parity with the TypeScript helper and for
// package-local callers; external callers should use ClassifyError.
func errorRecord(err error) ErrorRecord { return ClassifyError(err) }

// MarshalError serializes one structured failure without a trailing newline.
func MarshalError(err error) ([]byte, error) { return json.Marshal(ClassifyError(err)) }

// FormatJSONError serializes one structured failure and appends the newline
// required by the command-line JSON contract.
func FormatJSONError(err error) string {
	data, marshalErr := MarshalError(err)
	if marshalErr != nil {
		// ErrorRecord contains only JSON-safe scalar fields, so this is defensive
		// and keeps the writer usable even if its representation changes later.
		return `{"status":"error","code":"OPERATION_FAILED","message":"could not encode error","retryable":false}` + "\n"
	}
	return string(data) + "\n"
}

// JSONError is a concise alias for FormatJSONError.
func JSONError(err error) string { return FormatJSONError(err) }

// FormatHumanError preserves the entrypoint's human-mode stderr shape.
func FormatHumanError(err error) string { return "ruk: " + errorDetail(err) + "\n" }

// HumanError is a concise alias for FormatHumanError.
func HumanError(err error) string { return FormatHumanError(err) }

// WriteError writes exactly one human or JSON failure record to destination.
func WriteError(destination io.Writer, err error, jsonMode bool) error {
	if jsonMode {
		_, writeErr := io.WriteString(destination, FormatJSONError(err))
		return writeErr
	}
	_, writeErr := io.WriteString(destination, FormatHumanError(err))
	return writeErr
}

// JSONRequested keeps run, exec, and shell child arguments outside Ruk's JSON
// option scan. A delimiter likewise ends the scan for every other command.
func JSONRequested(argv []string) bool {
	if len(argv) > 0 && (argv[0] == "run" || argv[0] == "exec" || argv[0] == "shell") {
		return false
	}
	end := len(argv)
	for index, argument := range argv {
		if argument == "--" {
			end = index
			break
		}
	}
	for _, argument := range argv[:end] {
		if argument == "--json" {
			return true
		}
	}
	return false
}

// jsonRequested is the package-local form used by CLI entrypoint code.
func jsonRequested(argv []string) bool { return JSONRequested(argv) }

var (
	assignmentConflictPattern = regexp.MustCompile(`(?i)assignment .* (does not exist|no longer owns)|expected (assigned|returning)|(preparation|acquisition|collection) operation does not match|workspace .* is not managed`)
	portUnavailablePattern    = regexp.MustCompile(`(?i)port .* unavailable|allocate an available port|allocator returned`)
	resourceBusyPattern       = regexp.MustCompile(`(?i)lock|acquisition is still in progress|changed before collection|still has tracked processes|survived graceful termination|could not enumerate .* processes|process identity|process table|descendant probe|could not be identified, so its workspace cannot be released safely|leader is missing or reused|activity renewal failed|primary checkout has|shared checkout`)
	dependencyPattern         = regexp.MustCompile(`(?i)install|dependency|node_modules projection|shared dependency backend`)
	gitPattern                = regexp.MustCompile(`(?i)(^|\s)(git|worktree)\b|branch .* checked out|remote .* does not exist`)
)

func classifyMessage(message string) (ErrorCode, bool) {
	lower := strings.ToLower(message)

	// Package-manager discovery is part of dependency preparation. Preserve
	// the TypeScript automation contract instead of reporting a generic
	// operation failure when the repository's selected manager is absent.
	if strings.Contains(lower, "is required but was not found on path") {
		return DependencyPreparationCode, true
	}
	// Shared Bun/pnpm preflight and version checks are dependency preparation,
	// even when their wording contains "requires", which otherwise belongs to
	// invalid-argument validation.
	if strings.Contains(lower, "shared dependency backend") || strings.Contains(lower, "global virtual store requires") {
		return DependencyPreparationCode, true
	}

	// Parse/configuration failures are intentionally checked before broad
	// dependency and Git terms, as in the TypeScript classifier.
	if strings.Contains(lower, ".rukrc.json") ||
		strings.Contains(lower, "unknown option") ||
		strings.Contains(lower, "unknown command") ||
		strings.Contains(lower, "requires") ||
		strings.Contains(lower, "does not accept") ||
		strings.Contains(lower, "must be") ||
		strings.Contains(lower, "must contain") ||
		strings.Contains(lower, "exactly one") ||
		strings.Contains(lower, "command is required") ||
		strings.Contains(lower, "cannot be empty") {
		return InvalidArgumentCode, false
	}
	if assignmentConflictPattern.MatchString(message) {
		return AssignmentConflictCode, false
	}
	if strings.Contains(lower, "uncommitted changes") || strings.Contains(lower, "workspace ") && strings.Contains(lower, " dirty") {
		return WorkspaceDirtyCode, false
	}
	if portUnavailablePattern.MatchString(message) {
		return PortUnavailableCode, true
	}
	if resourceBusyPattern.MatchString(message) {
		return ResourceBusyCode, true
	}
	if dependencyPattern.MatchString(message) {
		return DependencyPreparationCode, true
	}
	if gitPattern.MatchString(message) {
		return GitOperationFailedCode, false
	}
	return OperationFailedCode, false
}

func errorDetail(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
