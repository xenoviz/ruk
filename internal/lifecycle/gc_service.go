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
	Reader       GCStateReader
	Lifecycle    *Service
	Release      GCReleaseOperation
	Git          GCWorkspaceGit
	TreeState    GCTreeStateDeleter
	Locker       ReleaseLocker
	LocksRoot    string
	Canonicalize GCPathCanonicalizer
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
	err = service.options.Locker.With(ctx, lockPath+".acquire", func() error {
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
			released, releaseErr := service.options.Release.ReleaseAssignment(ctx, workspace.Assignment.ID, ReleaseOptions{Force: true, RequireExpiredBy: collectionTime, ExpectedUpdatedAt: workspace.UpdatedAt})
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
			released, releaseErr := service.options.Release.ReleaseAssignment(ctx, workspace.Assignment.ID, ReleaseOptions{Force: true, AcquisitionOperationID: *workspace.OperationID, ExpectedUpdatedAt: workspace.UpdatedAt})
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
			return service.restoreCollection(ctx, collecting.Path, operationID, err, false)
		}
		if err := service.options.Git.Remove(ctx, collecting.Path, true); err != nil {
			return service.restoreCollection(ctx, collecting.Path, operationID, err, true)
		}
	}
	// Once Git removal succeeds, leave the collection fence in place if state
	// deletion fails. The interrupted collection remains retryable and cannot
	// be mistaken for reusable capacity.
	if _, err := service.options.Lifecycle.DeleteWorkspaceRecord(ctx, collecting.Path, operationID); err != nil {
		return err
	}
	if err := service.options.TreeState.DeleteTreeState(ctx, collecting.Path); err != nil {
		return err
	}
	_ = candidate
	return nil
}

func (service *GCService) restoreCollection(ctx context.Context, path, operationID string, cause error, unlocked bool) error {
	if unlocked {
		if relockErr := service.options.Git.Lock(ctx, path); relockErr != nil {
			return errors.Join(cause, fmt.Errorf("relock worktree %s after failed removal: %w", path, relockErr))
		}
	}
	if _, cancelErr := service.options.Lifecycle.CancelWorkspaceCollection(ctx, path, operationID); cancelErr != nil {
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
	return samePath(current, candidate), nil
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
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	return leftErr == nil && rightErr == nil && leftAbs == rightAbs
}

func isStaleGCError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "changed before collection") || strings.Contains(message, "changed before") || strings.Contains(message, "does not exist") || strings.Contains(message, "operation does not match") || strings.Contains(message, "was renewed before collection")
}
