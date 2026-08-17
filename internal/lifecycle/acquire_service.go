package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/xenoviz/ruk/internal/dependencies"
	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

// AcquisitionLocker is the per-workspace lock used for the complete
// acquisition handoff. The lock must remain held while Git, dependency, and
// port operations run and until the final lifecycle transition is published.
type AcquisitionLocker interface {
	With(context.Context, string, func() error) error
}

// AcquisitionStateReader supplies a read-only state snapshot for candidate
// selection. Selection must not acquire the state writer lock or rewrite the
// state file before the workspace handoff lock is held.
type AcquisitionStateReader interface {
	Read(context.Context) (*state.State, error)
}

// AcquisitionWorktree is the narrow Git seam needed by Acquire. Implementors
// should delegate to git.WorkspaceService or git.Client; this service never
// invokes a subprocess directly.
type AcquisitionWorktree interface {
	Create(context.Context, string, string, string) error
	Lock(context.Context, string) error
	Assign(context.Context, string, string, string) error
}

// DependencyPreparer prepares one workspace and returns the stable dependency
// metadata that is useful to the caller's handoff response.
type DependencyPreparer func(context.Context, string) (dependencies.EnsureResult, error)

// PortAllocator publishes named ports for an assignment while its acquisition
// operation marker is still active.
type PortAllocator interface {
	Allocate(context.Context, string, []string) (state.WorkspaceRecord, error)
}

// AcquisitionOptions contains all external seams used by AcquisitionService.
// Every operation is injected so orchestration tests and production adapters
// can use the same state/fencing behavior without coupling lifecycle to Git or
// an installer process.
type AcquisitionOptions struct {
	Lifecycle *Service
	Reader    AcquisitionStateReader
	Locker    AcquisitionLocker
	// PrimaryLocker fences assignment publication against deny-mode work in
	// the repository's primary checkout. It is acquired before Locker.
	PrimaryLocker   AcquisitionLocker
	PrimaryLockPath string
	// PoolMaintenanceLocker serializes reusable-slot selection with warm
	// capacity maintenance. Production DirectoryLocker also supports the
	// manual guard path, allowing this pool lock to be released before the
	// lengthy dependency handoff begins.
	PoolMaintenanceLocker   AcquisitionLocker
	PoolMaintenanceLockPath string
	Worktree                AcquisitionWorktree
	Prepare                 DependencyPreparer
	Ports                   PortAllocator

	// WorkspacePath creates a destination when no reusable workspace exists.
	// An explicit AcquisitionInput.WorkspacePath takes precedence.
	WorkspacePath func(context.Context, string) (string, error)

	// LockPath maps a workspace to its acquisition lock. In production this
	// should place locks below the repository's common Git directory. When nil,
	// a deterministic sidecar path is used, which is useful for adapters/tests.
	LockPath func(string) string

	// Cleanup resets an externally changed worktree after a failed handoff.
	// It is called while the acquisition lock is held. A nil cleanup is allowed
	// for adapters whose failed operation has no tree-side effects.
	Cleanup func(context.Context, string, bool) error
}

// AcquisitionService orchestrates one bounded workspace acquisition.
type AcquisitionService struct {
	lifecycle     *Service
	reader        AcquisitionStateReader
	locker        AcquisitionLocker
	primaryLocker AcquisitionLocker
	primaryPath   string
	poolLocker    AcquisitionLocker
	poolPath      string
	worktree      AcquisitionWorktree
	prepare       DependencyPreparer
	ports         PortAllocator
	workspacePath func(context.Context, string) (string, error)
	lockPath      func(string) string
	cleanup       func(context.Context, string, bool) error
}

// NewAcquisitionService constructs an acquisition orchestrator. Lifecycle is
// required because all ownership and handoff changes must pass through its
// operation-fenced transitions.
func NewAcquisitionService(options AcquisitionOptions) *AcquisitionService {
	if options.Lifecycle == nil {
		panic("lifecycle: nil acquisition lifecycle")
	}
	lockPath := options.LockPath
	if lockPath == nil {
		lockPath = func(path string) string { return filepath.Clean(path) + ".acquisition.lock" }
	}
	return &AcquisitionService{
		lifecycle:     options.Lifecycle,
		reader:        options.Reader,
		locker:        options.Locker,
		primaryLocker: options.PrimaryLocker,
		primaryPath:   options.PrimaryLockPath,
		poolLocker:    options.PoolMaintenanceLocker,
		poolPath:      options.PoolMaintenanceLockPath,
		worktree:      options.Worktree,
		prepare:       options.Prepare,
		ports:         options.Ports,
		workspacePath: options.WorkspacePath,
		lockPath:      lockPath,
		cleanup:       options.Cleanup,
	}
}

// AcquireInput identifies the requested branch, ownership lease, and named
// ports. WorkspacePath optionally narrows reuse to one path and is also used
// as the destination for a fresh workspace when no reusable slot is found.
type AcquireInput struct {
	Assignment    AssignmentInput
	Branch        string
	StartPoint    string
	WorkspacePath string
	PortNames     []string
}

// AcquisitionResult is the durable assignment plus dependency metadata from
// a successful handoff.
type AcquisitionResult struct {
	Workspace    state.WorkspaceRecord
	AssignmentID string
	Path         string
	Branch       string
	Reused       bool
	Fingerprint  string
	Mode         string
	Ports        map[string]int64
}

// Acquire reserves a valid available workspace or records a new preparing
// workspace, then performs the complete Git/dependency/port handoff under the
// workspace acquisition lock. Any failure retains an ID-fenced assignment or
// records failed preparation so abandoned work remains recoverable.
func (service *AcquisitionService) Acquire(ctx context.Context, input AcquireInput) (AcquisitionResult, error) {
	if service == nil || service.lifecycle == nil {
		return AcquisitionResult{}, errors.New("acquisition service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return AcquisitionResult{}, err
	}
	branch := input.Branch
	if branch == "" {
		branch = input.Assignment.Branch
	}
	if branch == "" {
		return AcquisitionResult{}, errors.New("branch must not be empty")
	}
	input.Assignment.Branch = branch
	if err := validateAssignmentInput(input.Assignment); err != nil {
		return AcquisitionResult{}, err
	}
	if service.locker == nil {
		return AcquisitionResult{}, errors.New("acquisition lock is not configured")
	}
	if service.reader == nil {
		return AcquisitionResult{}, errors.New("acquisition state reader is not configured")
	}
	if service.worktree == nil {
		return AcquisitionResult{}, errors.New("acquisition worktree service is not configured")
	}
	if service.prepare == nil {
		return AcquisitionResult{}, errors.New("dependency preparation is not configured")
	}
	if len(input.PortNames) > 0 && service.ports == nil {
		return AcquisitionResult{}, errors.New("port allocation is not configured")
	}

	if service.primaryLocker != nil && service.primaryPath == "" {
		return AcquisitionResult{}, errors.New("primary checkout lock path is not configured")
	}
	// Selection is deliberately read-only. Publishing the assigned operation
	// marker is done only after the repository fence and workspace handoff lock
	// are held.
	if service.primaryLocker != nil {
		var result AcquisitionResult
		var acquireErr error
		acquireErr = service.primaryLocker.With(ctx, service.primaryPath, func() error {
			result, acquireErr = service.acquireLoop(ctx, input)
			return acquireErr
		})
		return result, acquireErr
	}
	return service.acquireLoop(ctx, input)
}

func (service *AcquisitionService) acquireLoop(ctx context.Context, input AcquireInput) (AcquisitionResult, error) {
	if service.poolLocker != nil {
		if service.poolPath == "" {
			return AcquisitionResult{}, errors.New("pool maintenance lock path is not configured")
		}
		if manual, ok := service.poolLocker.(interface {
			Acquire(context.Context, string) (*lock.Guard, error)
		}); ok {
			return service.acquireWithPoolGuard(ctx, input, manual)
		}
		// Adapters without manual guards retain the same serialization contract;
		// production uses DirectoryLocker above so preparation is outside pool.
		var result AcquisitionResult
		var operationErr error
		operationErr = service.poolLocker.With(ctx, service.poolPath, func() error {
			result, operationErr = service.acquireLoopWithoutPool(ctx, input)
			return operationErr
		})
		return result, operationErr
	}
	return service.acquireLoopWithoutPool(ctx, input)
}

func (service *AcquisitionService) acquireLoopWithoutPool(ctx context.Context, input AcquireInput) (AcquisitionResult, error) {
	// Selection is deliberately read-only. Publishing the assigned operation
	// marker is done only after the selected workspace's handoff lock is held.
	for attempt := 0; attempt < 8; attempt++ {
		candidate, selectErr := service.selectAvailable(ctx, input)
		if selectErr != nil {
			return AcquisitionResult{}, selectErr
		}
		if candidate == nil {
			return service.acquireFresh(ctx, input)
		}
		result, reserveErr := service.acquireReserved(ctx, input, *candidate)
		if errors.Is(reserveErr, errReservationChanged) {
			continue
		}
		return result, reserveErr
	}
	return AcquisitionResult{}, errors.New("available workspace changed during acquisition; retry")
}

func (service *AcquisitionService) acquireWithPoolGuard(ctx context.Context, input AcquireInput, manual interface {
	Acquire(context.Context, string) (*lock.Guard, error)
}) (AcquisitionResult, error) {
	for attempt := 0; attempt < 8; attempt++ {
		poolGuard, err := manual.Acquire(ctx, service.poolPath)
		if err != nil {
			return AcquisitionResult{}, err
		}
		candidate, selectErr := service.selectAvailable(ctx, input)
		if selectErr != nil {
			return AcquisitionResult{}, errors.Join(selectErr, releaseAcquisitionGuard(ctx, poolGuard))
		}
		if candidate == nil {
			return service.releasePoolAndAcquireFresh(ctx, input, poolGuard)
		}
		workspaceGuard, lockErr := service.acquireWorkspaceGuard(ctx, candidate.Path)
		if lockErr != nil {
			return AcquisitionResult{}, errors.Join(lockErr, releaseAcquisitionGuard(ctx, poolGuard))
		}
		assigned, reserveErr := service.reserveReserved(ctx, input, *candidate)
		if reserveErr != nil {
			workspaceReleaseErr := releaseAcquisitionGuard(ctx, workspaceGuard)
			if errors.Is(reserveErr, errReservationChanged) {
				poolReleaseErr := releaseAcquisitionGuard(ctx, poolGuard)
				if workspaceReleaseErr != nil || poolReleaseErr != nil {
					return AcquisitionResult{}, errors.Join(reserveErr, workspaceReleaseErr, poolReleaseErr)
				}
				continue
			}
			return AcquisitionResult{}, errors.Join(reserveErr, workspaceReleaseErr, releaseAcquisitionGuard(ctx, poolGuard))
		}
		if releaseErr := releaseAcquisitionGuard(ctx, poolGuard); releaseErr != nil {
			recoveryErr := service.failAssigned(ctx, assigned.Assignment.ID, *assigned.OperationID, assigned.Path, false, fmt.Errorf("pool maintenance lock release failed: %w", releaseErr))
			workspaceReleaseErr := releaseAcquisitionGuard(ctx, workspaceGuard)
			return AcquisitionResult{}, errors.Join(recoveryErr, workspaceReleaseErr)
		}
		result, handoffErr := service.completeReserved(ctx, input, *candidate, assigned)
		return result, acquisitionReleaseFailure(result, handoffErr, releaseAcquisitionGuard(ctx, workspaceGuard))
	}
	return AcquisitionResult{}, errors.New("available workspace changed during acquisition; retry")
}

func (service *AcquisitionService) releasePoolAndAcquireFresh(ctx context.Context, input AcquireInput, guard *lock.Guard) (AcquisitionResult, error) {
	if err := releaseAcquisitionGuard(ctx, guard); err != nil {
		return AcquisitionResult{}, err
	}
	return service.acquireFresh(ctx, input)
}

const acquisitionGuardReleaseAttempts = 3

// releaseAcquisitionGuard makes a bounded best effort to remove a guard. A
// transient owner-file or filesystem failure must not leave a pool lock held
// indefinitely; callers retain the final joined error and fail closed.
func releaseAcquisitionGuard(ctx context.Context, guard *lock.Guard) error {
	if guard == nil {
		return errors.New("acquisition lock guard is nil")
	}
	var releaseErr error
	for attempt := 0; attempt < acquisitionGuardReleaseAttempts; attempt++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return errors.Join(releaseErr, err)
			}
		}
		if err := guard.Release(); err == nil {
			return nil
		} else {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	return releaseErr
}

func (service *AcquisitionService) acquireWorkspaceGuard(ctx context.Context, path string) (*lock.Guard, error) {
	manual, ok := service.locker.(interface {
		Acquire(context.Context, string) (*lock.Guard, error)
	})
	if !ok {
		return nil, errors.New("workspace lock does not support manual acquisition")
	}
	return manual.Acquire(ctx, service.lockPath(path))
}

var errReservationChanged = errors.New("available workspace reservation changed")

func (service *AcquisitionService) selectAvailable(ctx context.Context, input AcquireInput) (*state.WorkspaceRecord, error) {
	requestedKey := ""
	if input.WorkspacePath != "" {
		resolved, err := filepath.Abs(filepath.Clean(input.WorkspacePath))
		if err != nil {
			return nil, fmt.Errorf("resolve workspace path: %w", err)
		}
		requestedKey, err = state.TreeKey(resolved)
		if err != nil {
			return nil, err
		}
	}
	current, err := service.reader.Read(ctx)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("acquisition state reader returned nil state")
	}
	candidates := make([]state.WorkspaceRecord, 0)
	for key, workspace := range current.Workspaces {
		if workspace.Lifecycle != state.LifecycleAvailable || workspace.OperationID != nil {
			continue
		}
		if requestedKey != "" && key != requestedKey {
			continue
		}
		candidates = append(candidates, workspace)
	}
	sort.Slice(candidates, func(left, right int) bool {
		leftAvailable, rightAvailable := "", ""
		if candidates[left].AvailableAt != nil {
			leftAvailable = *candidates[left].AvailableAt
		}
		if candidates[right].AvailableAt != nil {
			rightAvailable = *candidates[right].AvailableAt
		}
		return leftAvailable < rightAvailable || (leftAvailable == rightAvailable && candidates[left].Path < candidates[right].Path)
	})
	var selected *state.WorkspaceRecord
	if len(candidates) > 0 {
		copy := candidates[0]
		selected = &copy
	}
	return selected, nil
}

func (service *AcquisitionService) acquireReserved(ctx context.Context, input AcquireInput, reserved state.WorkspaceRecord) (result AcquisitionResult, err error) {
	path := reserved.Path
	handoff := func() error {
		assigned, reserveErr := service.reserveReserved(ctx, input, reserved)
		if reserveErr != nil {
			return reserveErr
		}
		result, err = service.completeReserved(ctx, input, reserved, assigned)
		return err
	}
	if err = service.locker.With(ctx, service.lockPath(path), handoff); err != nil {
		return result, acquisitionReleaseFailure(result, err, nil)
	}
	return result, nil
}

func (service *AcquisitionService) reserveReserved(ctx context.Context, input AcquireInput, reserved state.WorkspaceRecord) (state.WorkspaceRecord, error) {
	assigned, err := service.lifecycle.ReserveAvailableWorkspace(ctx, input.Assignment, reserved.Path)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	if assigned == nil {
		return state.WorkspaceRecord{}, errReservationChanged
	}
	if assigned.Assignment == nil || assigned.OperationID == nil {
		return state.WorkspaceRecord{}, errors.New("reserved workspace has no assignment handoff fence")
	}
	return *assigned, nil
}

func (service *AcquisitionService) completeReserved(ctx context.Context, input AcquireInput, reserved state.WorkspaceRecord, assigned state.WorkspaceRecord) (result AcquisitionResult, err error) {
	path := reserved.Path
	assignmentID := assigned.Assignment.ID
	operationID := *assigned.OperationID
	expectedRenewedAt := assigned.Assignment.RenewedAt
	expectedExpiresAt := assigned.Assignment.ExpiresAt
	if err := service.verifyAssignment(ctx, assignmentID, operationID); err != nil {
		return result, err
	}
	if err := service.worktree.Assign(ctx, path, input.Assignment.Branch, input.StartPoint); err != nil {
		return result, service.failAssigned(ctx, assignmentID, operationID, path, true, err)
	}
	dependency, prepErr := service.prepare(ctx, path)
	if prepErr != nil {
		return result, service.failAssigned(ctx, assignmentID, operationID, path, true, prepErr)
	}
	if err := service.verifyAssignment(ctx, assignmentID, operationID); err != nil {
		return result, service.failAssigned(ctx, assignmentID, operationID, path, true, err)
	}
	if err := service.allocate(ctx, assignmentID, input.PortNames); err != nil {
		return result, service.failAssigned(ctx, assignmentID, operationID, path, true, err)
	}
	if input.Assignment.LeaseDurationMinutes > 0 {
		if _, err := service.lifecycle.RefreshAcquisitionLease(ctx, assignmentID, operationID, expectedRenewedAt, expectedExpiresAt, input.Assignment.LeaseDurationMinutes); err != nil {
			return result, service.failAssigned(ctx, assignmentID, operationID, path, true, err)
		}
	}
	workspace, successErr := service.lifecycle.RecordAcquisitionSuccess(ctx, assignmentID, operationID, true)
	if successErr != nil {
		return result, service.failAssigned(ctx, assignmentID, operationID, path, true, successErr)
	}
	return makeAcquisitionResult(workspace, dependency, true), nil
}

func (service *AcquisitionService) acquireFresh(ctx context.Context, input AcquireInput) (result AcquisitionResult, err error) {
	path := input.WorkspacePath
	if path == "" {
		if service.workspacePath == nil {
			return result, errors.New("workspace path is required when no reusable workspace exists")
		}
		path, err = service.workspacePath(ctx, input.Assignment.Branch)
		if err != nil {
			return result, err
		}
	}
	path, err = filepath.Abs(filepath.Clean(path))
	if err != nil {
		return result, fmt.Errorf("resolve workspace path: %w", err)
	}
	var preparationID string
	var preparationStarted bool
	handoff := func() error {
		preparing, beginErr := service.lifecycle.BeginPreparation(ctx, path, input.Assignment.Branch)
		if beginErr != nil {
			return beginErr
		}
		preparationStarted = true
		path = preparing.Path
		if preparing.OperationID == nil {
			return errors.New("preparing workspace has no operation fence")
		}
		preparationID = *preparing.OperationID
		if err := service.verifyPreparation(ctx, path, preparationID); err != nil {
			return err
		}
		if err := service.worktree.Create(ctx, path, input.Assignment.Branch, input.StartPoint); err != nil {
			return service.failPreparation(ctx, path, preparationID, true, err)
		}
		if err := service.worktree.Lock(ctx, path); err != nil {
			return service.failPreparation(ctx, path, preparationID, true, fmt.Errorf("lock fresh worktree %s: %w", path, err))
		}
		if err := service.worktree.Assign(ctx, path, input.Assignment.Branch, input.StartPoint); err != nil {
			return service.failPreparation(ctx, path, preparationID, true, err)
		}
		dependency, prepErr := service.prepare(ctx, path)
		if prepErr != nil {
			return service.failPreparation(ctx, path, preparationID, true, prepErr)
		}
		if err := service.verifyPreparation(ctx, path, preparationID); err != nil {
			return service.failPreparation(ctx, path, preparationID, true, err)
		}
		assigned, assignErr := service.lifecycle.MarkAssigned(ctx, path, preparationID, input.Assignment)
		if assignErr != nil {
			return service.failPreparation(ctx, path, preparationID, true, assignErr)
		}
		if assigned.Assignment == nil || assigned.OperationID == nil {
			return service.failFreshAssigned(ctx, assigned, true, errors.New("assigned workspace has no acquisition fence"))
		}
		assignmentID := assigned.Assignment.ID
		operationID := *assigned.OperationID
		expectedRenewedAt := assigned.Assignment.RenewedAt
		expectedExpiresAt := assigned.Assignment.ExpiresAt
		if err := service.allocate(ctx, assignmentID, input.PortNames); err != nil {
			return service.failFreshAssigned(ctx, assigned, true, err)
		}
		if input.Assignment.LeaseDurationMinutes > 0 {
			if _, err := service.lifecycle.RefreshAcquisitionLease(ctx, assignmentID, operationID, expectedRenewedAt, expectedExpiresAt, input.Assignment.LeaseDurationMinutes); err != nil {
				return service.failFreshAssigned(ctx, assigned, true, err)
			}
		}
		workspace, successErr := service.lifecycle.RecordAcquisitionSuccess(ctx, assignmentID, operationID, false)
		if successErr != nil {
			return service.failFreshAssigned(ctx, assigned, true, successErr)
		}
		result = makeAcquisitionResult(workspace, dependency, false)
		return nil
	}
	if err = service.locker.With(ctx, service.lockPath(path), handoff); err != nil {
		// A failed lock acquisition occurs before BeginPreparation and therefore
		// has no lifecycle marker to repair. Once preparation starts, the
		// callback owns all failure transitions, including lock-release errors.
		if !preparationStarted {
			return result, err
		}
		return result, acquisitionReleaseFailure(result, err, nil)
	}
	return result, nil
}

func (service *AcquisitionService) allocate(ctx context.Context, assignmentID string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	_, err := service.ports.Allocate(ctx, assignmentID, append([]string(nil), names...))
	return err
}

func (service *AcquisitionService) verifyAssignment(ctx context.Context, assignmentID, operationID string) error {
	return service.lifecycle.store.Update(ctx, func(current *state.State) error {
		_, workspace, exists := findAssignment(current, assignmentID)
		if !exists {
			return fmt.Errorf("Assignment %s does not exist", assignmentID)
		}
		if workspace.Lifecycle != state.LifecycleAssigned {
			return fmt.Errorf("Workspace %s is %s, expected assigned", workspace.Path, workspace.Lifecycle)
		}
		if workspace.OperationID == nil || *workspace.OperationID != operationID {
			return errors.New("Acquisition operation does not match")
		}
		return nil
	})
}

func (service *AcquisitionService) verifyPreparation(ctx context.Context, path, operationID string) error {
	resolved, key, err := collectionKey(path)
	if err != nil {
		return err
	}
	return service.lifecycle.store.Update(ctx, func(current *state.State) error {
		workspace, exists := current.Workspaces[key]
		if !exists {
			return fmt.Errorf("Workspace %s is not managed", resolved)
		}
		if workspace.Lifecycle != state.LifecyclePreparing {
			return fmt.Errorf("Workspace %s is %s, expected preparing", workspace.Path, workspace.Lifecycle)
		}
		if workspace.OperationID == nil || *workspace.OperationID != operationID {
			return errors.New("Preparation operation does not match")
		}
		return nil
	})
}

func (service *AcquisitionService) failAssigned(ctx context.Context, assignmentID, operationID, path string, cleanup bool, cause error) error {
	if cause == nil {
		cause = errors.New("acquisition failed")
	}
	// Never clean a tree after ownership has crossed the immutable fence. A
	// delayed failure may belong to an old handoff; leaving its marker for
	// fenced recovery is safer than mutating a newer owner's workspace.
	if fenceErr := service.verifyAssignment(ctx, assignmentID, operationID); fenceErr != nil {
		return errors.Join(cause, fenceErr)
	}
	cleanupErr := error(nil)
	if cleanup && service.cleanup != nil {
		cleanupErr = service.cleanup(ctx, path, true)
	}
	retained, retainErr := service.lifecycle.RetainAssignmentAfterAcquisitionFailure(ctx, assignmentID, operationID, failureText(cause, cleanupErr))
	if retainErr != nil {
		return errors.Join(cause, cleanupErr, retainErr)
	}
	retainedCause := errors.Join(cause, cleanupErr)
	recovery := &RetainedAssignmentError{
		AssignmentID: assignmentID,
		Path:         path,
		Recovery:     "ruk release " + assignmentID,
		Cause:        retainedCause,
	}
	if retained.Assignment != nil {
		recovery.ExpiresAt = retained.Assignment.ExpiresAt
	}
	return recovery
}

func (service *AcquisitionService) failPreparation(ctx context.Context, path, operationID string, cleanup bool, cause error) error {
	if cause == nil {
		cause = errors.New("acquisition failed")
	}
	cleanupErr := error(nil)
	if cleanup && service.cleanup != nil {
		cleanupErr = service.cleanup(ctx, path, false)
	}
	_, markErr := service.lifecycle.MarkFailed(ctx, path, operationID, failureText(cause, cleanupErr))
	return errors.Join(cause, cleanupErr, markErr)
}

func (service *AcquisitionService) failFreshAssigned(ctx context.Context, assigned state.WorkspaceRecord, cleanup bool, cause error) error {
	if assigned.Assignment == nil || assigned.OperationID == nil {
		return cause
	}
	return service.failAssigned(ctx, assigned.Assignment.ID, *assigned.OperationID, assigned.Path, cleanup, cause)
}

func failureText(cause, cleanup error) string {
	if cleanup == nil {
		return cause.Error()
	}
	return fmt.Sprintf("%s; cleanup failed: %v", cause, cleanup)
}

func makeAcquisitionResult(workspace state.WorkspaceRecord, dependency dependencies.EnsureResult, reused bool) AcquisitionResult {
	result := AcquisitionResult{
		Workspace:   workspace,
		Reused:      reused,
		Fingerprint: dependency.Fingerprint,
		Mode:        dependency.Mode,
		Ports:       map[string]int64{},
	}
	if workspace.Assignment != nil {
		result.AssignmentID = workspace.Assignment.ID
		result.Ports = workspace.Assignment.Ports
	}
	result.Path = workspace.Path
	result.Branch = workspace.Branch
	return result
}

// acquisitionReleaseFailure preserves assignment recovery metadata when a
// handoff succeeded but its lock guard could not be released. The lifecycle
// transition is already committed in that case, so retrying the assignment
// transition would be unsafe; the typed error directs recovery through the
// normal release command instead.
func acquisitionReleaseFailure(result AcquisitionResult, handoffErr, releaseErr error) error {
	if handoffErr != nil {
		var retained *RetainedAssignmentError
		if releaseErr == nil && result.Workspace.Assignment != nil && result.AssignmentID != "" && !errors.As(handoffErr, &retained) {
			return &RetainedAssignmentError{
				AssignmentID: result.AssignmentID,
				Path:         result.Path,
				ExpiresAt:    result.Workspace.Assignment.ExpiresAt,
				Recovery:     "ruk release " + result.AssignmentID,
				Cause:        handoffErr,
			}
		}
		if releaseErr == nil {
			return handoffErr
		}
		return errors.Join(handoffErr, releaseErr)
	}
	if releaseErr == nil {
		return nil
	}
	if result.Workspace.Assignment == nil || result.AssignmentID == "" {
		return releaseErr
	}
	return &RetainedAssignmentError{
		AssignmentID: result.AssignmentID,
		Path:         result.Path,
		ExpiresAt:    result.Workspace.Assignment.ExpiresAt,
		Recovery:     "ruk release " + result.AssignmentID,
		Cause:        releaseErr,
	}
}
