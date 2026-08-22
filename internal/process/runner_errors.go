package process

import (
	"fmt"

	"github.com/xenoviz/ruk/internal/state"
)

// RegistrationError preserves both the registration failure and any cleanup
// failure. A cleanup failure means the process remains owned for recovery.
type RegistrationError struct {
	Register error
	Cleanup  error
}

// ProcessSetupError reports a failure after the child was started but before
// registration could hand it to the caller. A non-nil Cleanup means the full
// child boundary was not proven drained; callers must retain the assignment,
// using the durable unverified sentinel when one was registered.
type ProcessSetupError struct {
	Cause   error
	Cleanup error
}

// ProcessCleanupUnsafeError means that a child was started, but its exact
// identity or process-group boundary could not be established. The runner
// cannot safely signal that child, so callers must retain the owning
// assignment for recovery instead of releasing the workspace.
type ProcessCleanupUnsafeError struct {
	PID    int
	Mode   ProcessMode
	Record state.TrackedProcessRecord
	Cause  error
}

func (err *ProcessCleanupUnsafeError) Error() string {
	if err.Cause == nil {
		return fmt.Sprintf("process: cleanup for child %d is unsafe", err.PID)
	}
	return fmt.Sprintf("process: cleanup for child %d is unsafe: %v", err.PID, err.Cause)
}

func (err *ProcessCleanupUnsafeError) Unwrap() error { return err.Cause }

// ProcessWaitError means the child did not produce a trustworthy exit status.
// Ordinary non-zero exits are deliberately not wrapped this way.
type ProcessWaitError struct{ Cause error }

func (err *ProcessWaitError) Error() string {
	if err == nil || err.Cause == nil {
		return "process: child wait failed"
	}
	return fmt.Sprintf("process: child wait failed: %v", err.Cause)
}

func (err *ProcessWaitError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func (err *ProcessSetupError) Error() string {
	if err.Cleanup == nil {
		return fmt.Sprintf("prepare managed process: %v", err.Cause)
	}
	return fmt.Sprintf("prepare managed process: %v; cleanup failed: %v", err.Cause, err.Cleanup)
}

func (err *ProcessSetupError) Unwrap() []error {
	if err.Cleanup == nil {
		return []error{err.Cause}
	}
	return []error{err.Cause, err.Cleanup}
}

func (err *RegistrationError) Error() string {
	if err.Cleanup == nil {
		return fmt.Sprintf("register managed process: %v", err.Register)
	}
	return fmt.Sprintf("register managed process: %v; cleanup failed: %v", err.Register, err.Cleanup)
}

func (err *RegistrationError) Unwrap() []error {
	if err.Cleanup == nil {
		return []error{err.Register}
	}
	return []error{err.Register, err.Cleanup}
}
