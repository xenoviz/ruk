package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

// ReleaseStateReader is the read side of the state seam used to locate an
// assignment before taking its workspace handoff lock. A state.Store
// satisfies this interface.
type ReleaseStateReader interface {
	Read(context.Context) (*state.State, error)
}

// ReleaseProcesser reports and terminates one Ruk-tracked process. Both
// methods must be identity-fenced by the implementation; a numeric PID alone
// is never sufficient to authorize cleanup.
type ReleaseProcesser interface {
	Exists(context.Context, state.TrackedProcessRecord) (bool, error)
	Terminate(context.Context, state.TrackedProcessRecord, bool) (bool, error)
}

// ReleaseProcessManager is the correctly expanded compatibility name for
// ReleaseProcesser.
type ReleaseProcessManager = ReleaseProcesser

// ReleaseGitter resets and cleans one managed worktree. Implementations must
// keep all Git execution behind their own injected command boundary.
type ReleaseGitter interface {
	ResetCleanReturn(context.Context, string, bool, []string) error
}

// ReleaseGit is the compatibility name for the Git return seam.
type ReleaseGit = ReleaseGitter

// ReleaseGitRelocker is an optional extension used to restore a pooled
// worktree lock when a Git return operation fails after changing it.
type ReleaseGitRelocker interface {
	Lock(context.Context, string) error
}

// ReleasePorter releases every host-port reservation fenced by an assignment.
// Release is best-effort after lifecycle publication has succeeded.
type ReleasePorter interface {
	Release(context.Context, string) error
}

// ReleasePortRegistry is the compatibility name for the port release seam.
type ReleasePortRegistry = ReleasePorter

// ReleaseLocker serializes release with acquisition handoff and all other
// workspace-mutating operations.
type ReleaseLocker interface {
	With(context.Context, string, func() error) error
}

// ReleaseServiceOptions configures the external seams of ReleaseService.
// LocksRoot is normally state.StorePaths(commonDir).Locks. LockPath can be
// supplied by integrations that use a different lock layout.
type ReleaseServiceOptions struct {
	Reader    ReleaseStateReader
	Processes ReleaseProcesser
	Git       ReleaseGitter
	Ports     ReleasePorter
	Locker    ReleaseLocker

	LocksRoot string
	LockPath  func(string) string

	// ProcessDrainTimeout and ProcessPollInterval bound post-termination
	// verification. Zero values select conservative production defaults.
	ProcessDrainTimeout time.Duration
	ProcessPollInterval time.Duration
}

// ReleaseOptions controls one assignment return.
type ReleaseOptions struct {
	Force                  bool
	AcquisitionOperationID string
	RequireExpiredBy       string
	ExpectedUpdatedAt      string
	PreservedProjections   []string
	// PreservedProjectionReader is evaluated after the workspace handoff lock
	// has been acquired and the assignment has been re-read. It lets callers
	// derive projection paths from a current state snapshot without racing an
	// assigned sync that uses the same fence.
	PreservedProjectionReader func(context.Context, state.WorkspaceRecord) ([]string, error)

	// handoffLockHeld is set only by GC after it has acquired the exact
	// workspace handoff lock. It keeps forced recovery from recursively
	// acquiring a non-reentrant directory lock while preserving the locked
	// state revalidation performed by ReleaseAssignment.
	handoffLockHeld bool
}

// ReleaseResult reports the reusable workspace and the number of tracked
// process records removed during the return.
type ReleaseResult struct {
	Workspace        state.WorkspaceRecord
	CleanedProcesses int
}

const (
	defaultProcessDrainTimeout = 2 * time.Second
	defaultProcessPollInterval = 25 * time.Millisecond
)

// ProcessDrainError means a tracked process could not be proved gone before
// the bounded verification window ended. Alive is true only when the native
// process manager repeatedly observed the tracked tree; false means identity
// or descendant inspection remained uncertain.
type ProcessDrainError struct {
	PID   int64
	Alive bool
	Cause error
}

func (err *ProcessDrainError) Error() string {
	if err.Cause == nil {
		return fmt.Sprintf("tracked process %d did not drain", err.PID)
	}
	return fmt.Sprintf("tracked process %d did not drain: %v", err.PID, err.Cause)
}

func (err *ProcessDrainError) Unwrap() error { return err.Cause }

// ReleaseService composes the lifecycle return transitions with bounded
// process, Git, port, and lock operations.
type ReleaseService struct {
	lifecycle *Service
	options   ReleaseServiceOptions
}

// NewReleaseService creates an orchestration service around an existing
// lifecycle Service. The lifecycle Service remains the sole owner of durable
// assignment transitions.
func NewReleaseService(lifecycleService *Service, options ReleaseServiceOptions) *ReleaseService {
	if lifecycleService == nil {
		panic("lifecycle: nil release lifecycle service")
	}
	return &ReleaseService{lifecycle: lifecycleService, options: options}
}

// ReleaseAssignment returns one exact assignment to available capacity. A
// failure after the returning fence is published restores assigned ownership
// and retains the failure for a later retry.
func (service *ReleaseService) ReleaseAssignment(ctx context.Context, assignmentID string, options ReleaseOptions) (ReleaseResult, error) {
	if service == nil || service.lifecycle == nil {
		return ReleaseResult{}, errors.New("lifecycle: release service is not configured")
	}
	if strings.TrimSpace(assignmentID) == "" {
		return ReleaseResult{}, errors.New("assignment ID must not be empty")
	}
	if service.options.Reader == nil {
		return ReleaseResult{}, errors.New("lifecycle: release state reader is not configured")
	}
	if service.options.Processes == nil || service.options.Git == nil || service.options.Ports == nil || service.options.Locker == nil {
		return ReleaseResult{}, errors.New("lifecycle: release dependencies are not configured")
	}

	workspace, err := releaseAssignmentWorkspace(ctx, service.options.Reader, assignmentID)
	if err != nil {
		return ReleaseResult{}, err
	}
	lockPath, err := service.releaseLockPath(workspace.Path)
	if err != nil {
		return ReleaseResult{}, err
	}

	var result ReleaseResult
	releaseLocked := func() error {
		// Re-read under the same lock used by acquisition. The state transition
		// below remains the final assignment-ID fence if the initial read was
		// stale.
		lockedWorkspace, readErr := releaseAssignmentWorkspace(ctx, service.options.Reader, assignmentID)
		if readErr != nil {
			return readErr
		}
		if lockedWorkspace.Path != workspace.Path {
			return fmt.Errorf("Assignment %s changed workspace before release", assignmentID)
		}
		if lockedWorkspace.Lifecycle == state.LifecycleAssigned && lockedWorkspace.OperationID != nil {
			if options.AcquisitionOperationID == "" {
				return &AcquisitionInProgressError{AssignmentID: assignmentID}
			}
			if *lockedWorkspace.OperationID != options.AcquisitionOperationID {
				return fmt.Errorf("Assignment %s acquisition operation does not match", assignmentID)
			}
		}

		preservedProjections := append([]string(nil), options.PreservedProjections...)
		if options.PreservedProjectionReader != nil {
			var projectionErr error
			preservedProjections, projectionErr = options.PreservedProjectionReader(ctx, lockedWorkspace)
			if projectionErr != nil {
				return projectionErr
			}
		}

		returning, beginErr := service.lifecycle.BeginWorkspaceReturnWithOptions(ctx, assignmentID, ReturnOptions{
			RequireExpiredBy:       options.RequireExpiredBy,
			AcquisitionOperationID: options.AcquisitionOperationID,
			ExpectedUpdatedAt:      options.ExpectedUpdatedAt,
		})
		if beginErr != nil {
			return beginErr
		}

		cleaned, cleanupErr := service.cleanProcesses(ctx, assignmentID, returning.Processes, options.Force)
		if cleanupErr != nil {
			return service.cancelRelease(ctx, assignmentID, cleanupErr)
		}
		if gitErr := service.options.Git.ResetCleanReturn(ctx, returning.Path, options.Force, append([]string(nil), preservedProjections...)); gitErr != nil {
			gitErr = service.relockAfterGitFailure(ctx, returning.Path, gitErr)
			return service.cancelRelease(ctx, assignmentID, gitErr)
		}
		available, finishErr := service.lifecycle.FinishWorkspaceReturn(ctx, assignmentID)
		if finishErr != nil {
			return service.cancelRelease(ctx, assignmentID, finishErr)
		}
		result = ReleaseResult{Workspace: available, CleanedProcesses: cleaned}
		// State publication is authoritative. A port-registry failure must not
		// roll an already-available assignment back to assigned; the registry
		// prunes inactive reservations on its next successful update.
		_ = service.options.Ports.Release(ctx, assignmentID)
		return nil
	}
	if options.handoffLockHeld {
		err = releaseLocked()
	} else {
		err = service.options.Locker.With(ctx, lockPath, releaseLocked)
	}
	if err != nil {
		return ReleaseResult{}, err
	}
	return result, nil
}

// Release is a short alias for callers that already use the service name as
// the operation name.
func (service *ReleaseService) Release(ctx context.Context, assignmentID string, options ReleaseOptions) (ReleaseResult, error) {
	return service.ReleaseAssignment(ctx, assignmentID, options)
}

// NewReleaseOrchestrator is an explicit orchestration-oriented constructor
// alias retained for integrations that use that terminology.
func NewReleaseOrchestrator(lifecycleService *Service, options ReleaseServiceOptions) *ReleaseService {
	return NewReleaseService(lifecycleService, options)
}

func (service *ReleaseService) releaseLockPath(workspacePath string) (string, error) {
	if service.options.LockPath != nil {
		path := service.options.LockPath(workspacePath)
		if strings.TrimSpace(path) == "" {
			return "", errors.New("lifecycle: release lock path is empty")
		}
		return path, nil
	}
	if strings.TrimSpace(service.options.LocksRoot) == "" {
		return "", errors.New("lifecycle: release locks root is not configured")
	}
	key, err := state.TreeKey(workspacePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(service.options.LocksRoot, "workspace-"+key+".lock"), nil
}

func releaseAssignmentWorkspace(ctx context.Context, reader ReleaseStateReader, assignmentID string) (state.WorkspaceRecord, error) {
	current, err := reader.Read(ctx)
	if err != nil {
		return state.WorkspaceRecord{}, fmt.Errorf("read assignment %s: %w", assignmentID, err)
	}
	if current == nil {
		return state.WorkspaceRecord{}, errors.New("read release state: nil state")
	}
	var result state.WorkspaceRecord
	count := 0
	for _, workspace := range current.Workspaces {
		if workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
			continue
		}
		result = cloneWorkspace(workspace)
		count++
	}
	if count == 0 {
		return state.WorkspaceRecord{}, fmt.Errorf("Assignment %s does not exist", assignmentID)
	}
	if count != 1 {
		return state.WorkspaceRecord{}, fmt.Errorf("Assignment %s is not unique", assignmentID)
	}
	return result, nil
}

// AcquisitionInProgressError is retryable: acquisition still owns the
// workspace handoff fence and release must not cross it.
type AcquisitionInProgressError struct{ AssignmentID string }

func (err *AcquisitionInProgressError) Error() string {
	return fmt.Sprintf("Assignment %s acquisition is still in progress", err.AssignmentID)
}

func (err *AcquisitionInProgressError) Retryable() bool { return true }

func (service *ReleaseService) cleanProcesses(ctx context.Context, assignmentID string, records []state.TrackedProcessRecord, force bool) (int, error) {
	cleaned := 0
	for _, record := range records {
		alive, err := service.options.Processes.Exists(ctx, record)
		if err != nil {
			return cleaned, fmt.Errorf("inspect tracked process %d: %w", record.PID, err)
		}
		if alive {
			if _, err := service.options.Processes.Terminate(ctx, record, false); err != nil {
				return cleaned, fmt.Errorf("terminate tracked process %d: %w", record.PID, err)
			}
			if drainErr := service.waitForProcessDrain(ctx, record); drainErr != nil {
				var observed *ProcessDrainError
				if !force || !errors.As(drainErr, &observed) || !observed.Alive {
					if !force && errors.As(drainErr, &observed) && observed.Alive {
						return cleaned, fmt.Errorf("Tracked process %d survived graceful termination; retry with --force: %w", record.PID, drainErr)
					}
					return cleaned, fmt.Errorf("verify tracked process %d termination: %w", record.PID, drainErr)
				}
				if _, err := service.options.Processes.Terminate(ctx, record, true); err != nil {
					return cleaned, fmt.Errorf("force terminate tracked process %d: %w", record.PID, err)
				}
				if forceDrainErr := service.waitForProcessDrain(ctx, record); forceDrainErr != nil {
					return cleaned, fmt.Errorf("verify tracked process %d force termination: %w", record.PID, forceDrainErr)
				}
			}
			cleaned++
		}
		if _, err := service.lifecycle.RemoveAssignmentProcess(ctx, assignmentID, record.PID, record.StartedAt); err != nil {
			return cleaned, err
		}
	}
	return cleaned, nil
}

func (service *ReleaseService) waitForProcessDrain(ctx context.Context, record state.TrackedProcessRecord) error {
	timeout := service.options.ProcessDrainTimeout
	if timeout <= 0 {
		timeout = defaultProcessDrainTimeout
	}
	interval := service.options.ProcessPollInterval
	if interval <= 0 {
		interval = defaultProcessPollInterval
	}
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	observedAlive := false
	for {
		alive, err := service.options.Processes.Exists(waitCtx, record)
		if err == nil && !alive {
			return nil
		}
		if err != nil {
			lastErr = err
			observedAlive = false
		} else {
			observedAlive = true
			lastErr = errors.New("tracked process tree is still present")
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if lastErr == nil {
				lastErr = waitCtx.Err()
			}
			return &ProcessDrainError{PID: record.PID, Alive: observedAlive, Cause: lastErr}
		case <-timer.C:
		}
	}
}

func (service *ReleaseService) relockAfterGitFailure(ctx context.Context, workspacePath string, gitErr error) error {
	relocker, ok := service.options.Git.(ReleaseGitRelocker)
	if !ok {
		return gitErr
	}
	if relockErr := relocker.Lock(ctx, workspacePath); relockErr != nil {
		return errors.Join(gitErr, fmt.Errorf("relock worktree %s after failed cleanup: %w", workspacePath, relockErr))
	}
	return gitErr
}

func (service *ReleaseService) cancelRelease(ctx context.Context, assignmentID string, cause error) error {
	if cause == nil {
		cause = errors.New("release failed")
	}
	_, cancelErr := service.lifecycle.CancelWorkspaceReturn(ctx, assignmentID, cause.Error())
	if cancelErr != nil {
		return errors.Join(cause, fmt.Errorf("restore assignment ownership: %w", cancelErr))
	}
	return cause
}
