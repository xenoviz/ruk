package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

// GCStateReader is the read side of the state seam used for planning and
// revalidating candidates under the warm lock.
type GCStateReader interface {
	Read(context.Context) (*state.State, error)
}

// GCWorkspaceGit is the bounded Git mutation seam used by collection. It
// deliberately does not expose a command runner or shell.
type GCWorkspaceGit interface {
	IsWorktree(context.Context, string) (bool, error)
	Unlock(context.Context, string) error
	Remove(context.Context, string, bool) error
	Lock(context.Context, string) error
}

// GCTreeStateDeleter removes dependency metadata after the workspace record
// has been safely collected.
type GCTreeStateDeleter interface {
	DeleteTreeState(context.Context, string) error
}

// GCReleaseOperation is the narrow release seam needed for abandoned
// acquisitions and forced-expiry cleanup.
type GCReleaseOperation interface {
	ReleaseAssignment(context.Context, string, ReleaseOptions) (ReleaseResult, error)
}

// GCPathCanonicalizer resolves a workspace path for the current-workspace
// safety check. Implementations should resolve links, not merely clean text.
type GCPathCanonicalizer func(context.Context, string) (string, error)

// GCServiceOptions configures GC's state, lock, release, Git, and tree-state
// seams. LocksRoot normally comes from state.StorePaths(commonDir).Locks.
type GCServiceOptions struct {
	Reader    GCStateReader
	Lifecycle *Service
	Release   GCReleaseOperation
	// Processes is used only to recover tracked processes on unassigned
	// preparing/failed workspaces. Assigned workspaces continue through the
	// normal release service, preserving its ownership and handoff behavior.
	Processes    ReleaseProcesser
	Git          GCWorkspaceGit
	TreeState    GCTreeStateDeleter
	Locker       ReleaseLocker
	LocksRoot    string
	Canonicalize GCPathCanonicalizer

	// ProcessDrainTimeout and ProcessPollInterval bound forced process
	// termination verification. Zero values select the release defaults.
	ProcessDrainTimeout time.Duration
	ProcessPollInterval time.Duration
}

// GCOptions controls one plan or apply operation.
type GCOptions struct {
	OlderThan            time.Time
	Now                  time.Time
	Apply                bool
	ForceExpired         bool
	CurrentWorkspacePath string
}

// GCRemovedRecord is one workspace removed by an applied collection.
type GCRemovedRecord struct {
	Path      string `json:"path"`
	Lifecycle string `json:"lifecycle"`
	Reason    string `json:"reason"`
}

// GCExpiredRecord reports an expired assignment that still requires explicit
// forced cleanup.
type GCExpiredRecord struct {
	Path         string `json:"path"`
	AssignmentID string `json:"assignmentId"`
	ExpiresAt    string `json:"expiresAt"`
}

// GCResult is the stable machine-readable plan/apply result.
type GCResult struct {
	Status  string            `json:"status"`
	Removed []GCRemovedRecord `json:"removed"`
	Expired []GCExpiredRecord `json:"expired"`
}

// GCService orchestrates candidate identification and fenced collection.
type GCService struct {
	options GCServiceOptions
}

// NewGCService constructs a GC orchestration service.
func NewGCService(options GCServiceOptions) *GCService {
	return &GCService{options: options}
}

// Run plans or applies safe and explicitly forced-expired collection.
func (service *GCService) Run(ctx context.Context, options GCOptions) (GCResult, error) {
	if service == nil {
		return GCResult{}, errors.New("lifecycle: GC service is not configured")
	}
	if options.ForceExpired && !options.Apply {
		return GCResult{}, errors.New("--force-expired requires --apply")
	}
	if service.options.Reader == nil || service.options.Lifecycle == nil || service.options.Locker == nil {
		return GCResult{}, errors.New("lifecycle: GC dependencies are not configured")
	}
	if options.Apply && (service.options.Git == nil || service.options.TreeState == nil) {
		return GCResult{}, errors.New("lifecycle: GC collection dependencies are not configured")
	}

	poolPath, warmPath, err := service.maintenanceLockPaths()
	if err != nil {
		return GCResult{}, err
	}
	var result GCResult
	err = service.options.Locker.With(ctx, poolPath, func() error {
		return service.options.Locker.With(ctx, warmPath, func() error {
			candidates, identifyErr := service.identify(ctx, options)
			if identifyErr != nil {
				return identifyErr
			}
			removed := make([]GCRemovedRecord, 0)
			if options.Apply {
				for _, candidate := range candidates {
					if candidate.RequiresForce && !options.ForceExpired {
						continue
					}
					if candidate.RequiresForce && options.ForceExpired {
						continue
					}
					current, currentErr := service.isCurrent(ctx, options.CurrentWorkspacePath, candidate.Workspace.Path)
					if currentErr != nil {
						return currentErr
					}
					if current {
						continue
					}
					collected, collectErr := service.applyCandidate(ctx, options, candidate)
					if collectErr != nil {
						return collectErr
					}
					if collected {
						removed = append(removed, GCRemovedRecord{Path: candidate.Workspace.Path, Lifecycle: string(candidate.Workspace.Lifecycle), Reason: gcReasonText(candidate.Reason)})
					}
				}
				if options.ForceExpired {
					for _, candidate := range candidates {
						if !candidate.RequiresForce {
							continue
						}
						current, currentErr := service.isCurrent(ctx, options.CurrentWorkspacePath, candidate.Workspace.Path)
						if currentErr != nil {
							return currentErr
						}
						if current {
							continue
						}
						collected, collectErr := service.applyCandidate(ctx, options, candidate)
						if collectErr != nil {
							return collectErr
						}
						if collected {
							removed = append(removed, GCRemovedRecord{Path: candidate.Workspace.Path, Lifecycle: string(candidate.Workspace.Lifecycle), Reason: "expired assignment (forced)"})
						}
					}
				}
			} else {
				for _, candidate := range candidates {
					if candidate.RequiresForce {
						continue
					}
					current, currentErr := service.isCurrent(ctx, options.CurrentWorkspacePath, candidate.Workspace.Path)
					if currentErr != nil {
						return currentErr
					}
					if current {
						continue
					}
					removed = append(removed, GCRemovedRecord{Path: candidate.Workspace.Path, Lifecycle: string(candidate.Workspace.Lifecycle), Reason: gcReasonText(candidate.Reason)})
				}
			}
			expired, expiredErr := service.expired(ctx, options)
			if expiredErr != nil {
				return expiredErr
			}
			status := "planned"
			if options.Apply {
				status = "collected"
			}
			result = GCResult{Status: status, Removed: removed, Expired: expired}
			return nil
		})
	})
	if err != nil {
		return GCResult{}, err
	}
	return result, nil
}

// Execute is an explicit synonym for Run.
func (service *GCService) Execute(ctx context.Context, options GCOptions) (GCResult, error) {
	return service.Run(ctx, options)
}

func (service *GCService) identify(ctx context.Context, options GCOptions) ([]GcCandidate, error) {
	current, err := service.options.Reader.Read(ctx)
	if err != nil {
		return nil, err
	}
	return IdentifyGCCandidates(current, options.OlderThan, options.Now, true)
}

func (service *GCService) expired(ctx context.Context, options GCOptions) ([]GCExpiredRecord, error) {
	candidates, err := service.identify(ctx, options)
	if err != nil {
		return nil, err
	}
	result := make([]GCExpiredRecord, 0)
	for _, candidate := range candidates {
		if !candidate.RequiresForce || candidate.Workspace.Assignment == nil {
			continue
		}
		result = append(result, GCExpiredRecord{Path: candidate.Workspace.Path, AssignmentID: candidate.Workspace.Assignment.ID, ExpiresAt: candidate.Workspace.Assignment.ExpiresAt})
	}
	return result, nil
}

func (service *GCService) applyCandidate(ctx context.Context, options GCOptions, candidate GcCandidate) (bool, error) {
	lockPath, err := service.workspaceLockPath(candidate.Workspace.Path)
	if err != nil {
		return false, err
	}
	var collected bool
	err = service.options.Locker.With(ctx, lockPath, func() error {
		current, readErr := service.currentCandidate(ctx, options, candidate)
		if readErr != nil {
			return readErr
		}
		if current == nil {
			return nil
		}
		workspace := current.Workspace
		if candidate.RequiresForce {
			if workspace.Assignment == nil {
				return nil
			}
			if service.options.Release == nil {
				return errors.New("lifecycle: GC release operation is not configured")
			}
			collectionTime := options.Now.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano)
			released, releaseErr := service.options.Release.ReleaseAssignment(ctx, workspace.Assignment.ID, ReleaseOptions{Force: true, RequireExpiredBy: collectionTime, ExpectedUpdatedAt: workspace.UpdatedAt, handoffLockHeld: true})
			if releaseErr != nil {
				if isStaleGCError(releaseErr) {
					return nil
				}
				return releaseErr
			}
			workspace = released.Workspace
		} else if candidate.Reason == GcAbandonedAcquisition {
			if workspace.Assignment == nil || workspace.OperationID == nil {
				return nil
			}
			if service.options.Release == nil {
				return errors.New("lifecycle: GC release operation is not configured")
			}
			released, releaseErr := service.options.Release.ReleaseAssignment(ctx, workspace.Assignment.ID, ReleaseOptions{Force: true, AcquisitionOperationID: *workspace.OperationID, ExpectedUpdatedAt: workspace.UpdatedAt, handoffLockHeld: true})
			if releaseErr != nil {
				if isStaleGCError(releaseErr) {
					return nil
				}
				return releaseErr
			}
			workspace = released.Workspace
		}
		if err := service.collectWorkspace(ctx, workspace, candidate); err != nil {
			return err
		}
		collected = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return collected, nil
}

func (service *GCService) currentCandidate(ctx context.Context, options GCOptions, expected GcCandidate) (*GcCandidate, error) {
	candidates, err := service.identify(ctx, options)
	if err != nil {
		return nil, err
	}
	for index := range candidates {
		candidate := candidates[index]
		if candidate.Reason != expected.Reason || !samePath(candidate.Workspace.Path, expected.Workspace.Path) {
			continue
		}
		if candidate.RequiresForce {
			if expected.ExpectedAssignmentID != nil && (candidate.ExpectedAssignmentID == nil || *candidate.ExpectedAssignmentID != *expected.ExpectedAssignmentID) {
				continue
			}
			return &candidate, nil
		}
		if candidate.ExpectedUpdatedAt != expected.ExpectedUpdatedAt || !sameOptional(candidate.ExpectedOperationID, expected.ExpectedOperationID) || !sameOptional(candidate.ExpectedAssignmentID, expected.ExpectedAssignmentID) {
			continue
		}
		return &candidate, nil
	}
	return nil, nil
}

func (service *GCService) collectWorkspace(ctx context.Context, workspace state.WorkspaceRecord, candidate GcCandidate) error {
	if workspace.Assignment == nil && (workspace.Lifecycle == state.LifecyclePreparing || workspace.Lifecycle == state.LifecycleFailed) {
		var err error
		workspace, err = service.drainUnassignedProcesses(ctx, workspace)
		if err != nil {
			return err
		}
		workspace, err = service.revalidateUnassignedWorkspace(ctx, workspace)
		if err != nil {
			return err
		}
	}
	collecting, err := service.options.Lifecycle.BeginWorkspaceCollection(ctx, workspace.Path, workspace.UpdatedAt)
	if err != nil {
		return err
	}
	operationID := ""
	if collecting.OperationID != nil {
		operationID = *collecting.OperationID
	}
	if operationID == "" {
		return errors.New("collection did not publish an operation fence")
	}
	worktree, err := service.options.Git.IsWorktree(ctx, collecting.Path)
	if err != nil {
		return service.restoreCollection(ctx, collecting.Path, operationID, err, false)
	}
	if worktree {
		if err := service.options.Git.Unlock(ctx, collecting.Path); err != nil {
			// Unlock may have changed Git state before reporting an error. Treat
			// that boundary as uncertain and relock before clearing the fence.
			return service.restoreCollection(ctx, collecting.Path, operationID, err, true)
		}
		if err := service.options.Git.Remove(ctx, collecting.Path, true); err != nil {
			return service.restoreCollection(ctx, collecting.Path, operationID, err, true)
		}
	}
	// Delete the tree projection first. The workspace record remains fenced
	// until both records are gone, so a projection failure or a later state
	// persistence failure leaves an interrupted collection that GC can retry.
	if err := service.options.TreeState.DeleteTreeState(ctx, collecting.Path); err != nil {
		return err
	}
	if _, err := service.options.Lifecycle.DeleteWorkspaceRecord(ctx, collecting.Path, operationID); err != nil {
		return err
	}
	_ = candidate
	return nil
}

// drainUnassignedProcesses proves that every exact persisted process identity
// is gone, force-terminating live processes before removing their records. It
// runs while the caller holds the workspace handoff lock, and every durable
// removal is fenced by the lifecycle operation and the latest UpdatedAt.
func (service *GCService) drainUnassignedProcesses(ctx context.Context, workspace state.WorkspaceRecord) (state.WorkspaceRecord, error) {
	if len(workspace.Processes) == 0 {
		return workspace, nil
	}
	if service.options.Processes == nil {
		return state.WorkspaceRecord{}, errors.New("lifecycle: GC process manager is not configured")
	}
	current := cloneWorkspace(workspace)
	for _, record := range append([]state.TrackedProcessRecord(nil), workspace.Processes...) {
		alive, err := service.options.Processes.Exists(ctx, record)
		if err != nil {
			return state.WorkspaceRecord{}, fmt.Errorf("inspect tracked process %d during GC: %w", record.PID, err)
		}
		if alive {
			if _, err := service.options.Processes.Terminate(ctx, record, true); err != nil {
				return state.WorkspaceRecord{}, fmt.Errorf("force terminate tracked process %d during GC: %w", record.PID, err)
			}
			if err := service.waitForGCProcessDrain(ctx, record); err != nil {
				return state.WorkspaceRecord{}, fmt.Errorf("verify tracked process %d termination during GC: %w", record.PID, err)
			}
		}
		updated, err := service.options.Lifecycle.RemoveUnassignedProcess(ctx, current.Path, current.Lifecycle, current.OperationID, current.UpdatedAt, record.PID, record.StartedAt)
		if err != nil {
			return state.WorkspaceRecord{}, fmt.Errorf("remove tracked process %d during GC: %w", record.PID, err)
		}
		current = updated
	}
	return current, nil
}

func (service *GCService) waitForGCProcessDrain(ctx context.Context, record state.TrackedProcessRecord) error {
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
	for {
		alive, err := service.options.Processes.Exists(waitCtx, record)
		if err != nil {
			return err
		}
		if !alive {
			return nil
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
			return &ProcessDrainError{PID: record.PID, Alive: true, Cause: waitCtx.Err()}
		case <-timer.C:
		}
	}
}

// revalidateUnassignedWorkspace detects any state change after process
// records were drained and before BeginWorkspaceCollection publishes the
// collection fence. A stale or uncertain record fails closed before Git is
// inspected or mutated.
func (service *GCService) revalidateUnassignedWorkspace(ctx context.Context, expected state.WorkspaceRecord) (state.WorkspaceRecord, error) {
	current, err := service.options.Reader.Read(ctx)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	key, err := state.TreeKey(expected.Path)
	if err != nil {
		return state.WorkspaceRecord{}, err
	}
	workspace, ok := current.Workspaces[key]
	if !ok || workspace.Path != expected.Path {
		return state.WorkspaceRecord{}, errors.New("workspace changed before collection")
	}
	if workspace.Lifecycle != expected.Lifecycle || workspace.Assignment != nil || len(workspace.Processes) != 0 || workspace.UpdatedAt != expected.UpdatedAt || !sameCollectionOperation(workspace.OperationID, expected.OperationID) {
		return state.WorkspaceRecord{}, errors.New("workspace changed before collection")
	}
	return cloneWorkspace(workspace), nil
}

func (service *GCService) restoreCollection(ctx context.Context, path, operationID string, cause error, unlocked bool) error {
	recoveryCtx, cancel := acquisitionRecoveryContext(ctx)
	defer cancel()
	if unlocked {
		if relockErr := service.options.Git.Lock(recoveryCtx, path); relockErr != nil {
			return errors.Join(cause, fmt.Errorf("relock worktree %s after failed removal: %w", path, relockErr))
		}
	}
	if _, cancelErr := service.options.Lifecycle.CancelWorkspaceCollection(recoveryCtx, path, operationID); cancelErr != nil {
		return errors.Join(cause, fmt.Errorf("restore collection state: %w", cancelErr))
	}
	return cause
}

func (service *GCService) isCurrent(ctx context.Context, currentPath, candidatePath string) (bool, error) {
	if currentPath == "" {
		return false, nil
	}
	canonical := service.options.Canonicalize
	if canonical == nil {
		canonical = func(_ context.Context, path string) (string, error) { return filepath.Abs(filepath.Clean(path)) }
	}
	current, currentErr := canonical(ctx, currentPath)
	if currentErr != nil {
		return false, currentErr
	}
	candidate, candidateErr := canonical(ctx, candidatePath)
	if candidateErr != nil {
		return false, candidateErr
	}
	return pathContains(candidate, current), nil
}

func (service *GCService) maintenanceLockPaths() (string, string, error) {
	if strings.TrimSpace(service.options.LocksRoot) == "" {
		return "", "", errors.New("lifecycle: GC locks root is not configured")
	}
	return filepath.Join(service.options.LocksRoot, "pool-maintenance.lock"), filepath.Join(service.options.LocksRoot, "warm.lock"), nil
}

func (service *GCService) workspaceLockPath(path string) (string, error) {
	key, err := state.TreeKey(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(service.options.LocksRoot, "workspace-"+key+".lock"), nil
}

func gcReasonText(reason GcCandidateReason) string {
	switch reason {
	case GcAbandonedPreparation:
		return "abandoned preparation"
	case GcAbandonedAcquisition:
		return "abandoned acquisition"
	case GcInterruptedCollection:
		return "interrupted collection"
	default:
		return "older than max age"
	}
}

func sameOptional(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func samePath(left, right string) bool {
	return samePathForPlatform(left, right, runtime.GOOS == "windows")
}

// pathContains reports whether child is the workspace itself or a path below
// it. GC receives the process working directory as CurrentWorkspacePath; an
// agent commonly runs from a subdirectory, so protecting only an exact path
// could remove the workspace that owns the current process.
func pathContains(parent, child string) bool {
	return pathContainsForPlatform(parent, child, runtime.GOOS == "windows")
}

func samePathForPlatform(left, right string, caseInsensitive bool) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	if caseInsensitive {
		return strings.EqualFold(leftAbs, rightAbs)
	}
	return leftAbs == rightAbs
}

func pathContainsForPlatform(parent, child string, caseInsensitive bool) bool {
	parentAbs, parentErr := filepath.Abs(filepath.Clean(parent))
	childAbs, childErr := filepath.Abs(filepath.Clean(child))
	if parentErr != nil || childErr != nil {
		return false
	}
	if caseInsensitive {
		parentAbs = strings.ToLower(parentAbs)
		childAbs = strings.ToLower(childAbs)
	}
	if samePathForPlatform(parentAbs, childAbs, false) {
		return true
	}
	relative, err := filepath.Rel(parentAbs, childAbs)
	if err != nil || relative == "" || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	separator := string(filepath.Separator)
	return !strings.HasPrefix(relative, ".."+separator)
}

func isStaleGCError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "changed before collection") || strings.Contains(message, "changed before") || strings.Contains(message, "does not exist") || strings.Contains(message, "operation does not match") || strings.Contains(message, "was renewed before collection")
}
