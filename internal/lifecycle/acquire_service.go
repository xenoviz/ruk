package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/xenoviz/ruk/internal/dependencies"
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
	Worktree  AcquisitionWorktree
	Prepare   DependencyPreparer
	Ports     PortAllocator

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
	var assignmentID, operationID string
	handoff := func() error {
		assigned, reserveErr := service.lifecycle.ReserveAvailableWorkspace(ctx, input.Assignment, path)
		if reserveErr != nil {
			return reserveErr
		}
		if assigned == nil {
			return errReservationChanged
		}
		if assigned.Assignment == nil || assigned.OperationID == nil {
			return errors.New("reserved workspace has no assignment handoff fence")
		}
		assignmentID = assigned.Assignment.ID
		operationID = *assigned.OperationID
		if err := service.verifyAssignment(ctx, assignmentID, operationID); err != nil {
			return err
		}
		if err := service.worktree.Assign(ctx, path, input.Assignment.Branch, input.StartPoint); err != nil {
			return service.failAssigned(ctx, assignmentID, operationID, path, true, err)
		}
		dependency, prepErr := service.prepare(ctx, path)
		if prepErr != nil {
			return service.failAssigned(ctx, assignmentID, operationID, path, true, prepErr)
		}
		if err := service.verifyAssignment(ctx, assignmentID, operationID); err != nil {
			return service.failAssigned(ctx, assignmentID, operationID, path, true, err)
		}
		if err := service.allocate(ctx, assignmentID, input.PortNames); err != nil {
			return service.failAssigned(ctx, assignmentID, operationID, path, true, err)
		}
		workspace, successErr := service.lifecycle.RecordAcquisitionSuccess(ctx, assignmentID, operationID, true)
		if successErr != nil {
			return service.failAssigned(ctx, assignmentID, operationID, path, true, successErr)
		}
		result = makeAcquisitionResult(workspace, dependency, true)
		return nil
	}
	if err = service.locker.With(ctx, service.lockPath(path), handoff); err != nil {
		return AcquisitionResult{}, err
	}
	return result, nil
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
		if err := service.allocate(ctx, assignmentID, input.PortNames); err != nil {
			return service.failFreshAssigned(ctx, assigned, true, err)
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
		return result, err
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
	_ = retained
	return errors.Join(cause, cleanupErr, retainErr)
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
