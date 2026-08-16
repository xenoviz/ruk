package lifecycle

import "fmt"

// RetainedAssignmentError reports that an acquisition failure left ownership
// fenced for recovery.  The fields are deliberately typed data rather than
// information encoded in an error message so CLI callers can preserve the
// original failure category while still giving operators an exact release
// instruction.
type RetainedAssignmentError struct {
	AssignmentID string
	Path         string
	ExpiresAt    string
	Recovery     string
	Cause        error
}

func (err *RetainedAssignmentError) Error() string {
	if err == nil {
		return "assignment retained"
	}
	message := "assignment retained"
	if err.AssignmentID != "" {
		message = fmt.Sprintf("Assignment %s retained", err.AssignmentID)
	}
	if err.Path != "" {
		message += fmt.Sprintf(" at %s", err.Path)
	}
	if err.ExpiresAt != "" {
		message += fmt.Sprintf(" (expires %s)", err.ExpiresAt)
	}
	if err.Cause != nil {
		message += fmt.Sprintf(": %s", err.Cause)
	}
	if err.Recovery != "" {
		message += fmt.Sprintf("; recover with %s", err.Recovery)
	}
	return message
}

func (err *RetainedAssignmentError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}
