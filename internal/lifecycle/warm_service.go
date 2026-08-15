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

// WarmStateReader supplies a read-only state snapshot for capacity counting.
// It intentionally does not use Store.Update; the maintenance locks serialize
// this snapshot with pool mutation performed by cooperating callers.
type WarmStateReader interface {
	Read(context.Context) (*state.State, error)
}

// WarmLocker is the common directory-lock seam used for pool maintenance and
// warm operations. Production callers should use one shared pool-maintenance
// path for warm, acquisition, and collection.
type WarmLocker interface {
	With(context.Context, string, func() error) error
}

// WarmWorkspaceService is the Git worktree seam needed to create detached,
// dependency-ready pool capacity. WorkspaceService satisfies this interface.
type WarmWorkspaceService interface {
	Create(context.Context, string, string, string, bool) error
	Lock(context.Context, string) error
}

// WarmHeadReader returns the current Git HEAD for every known worktree. Map
// keys may be absolute paths or state tree keys; both forms are accepted.
type WarmHeadReader func(context.Context) (map[string]string, error)

// WarmTargetHead resolves the requested start point to an immutable commit ID.
type WarmTargetHead func(context.Context, string) (string, error)

// WarmDependencyValidator checks the current dependency fingerprint and
// projection integrity for one recorded tree. Returning false excludes a slot
// from capacity; returning an error aborts warm without mutating that slot.
type WarmDependencyValidator func(context.Context, string, state.TreeRecord) (bool, error)

// WarmDependencyPreparer prepares one new pool workspace. It must publish its
// dependency record through the supplied dependency/state seams.
type WarmDependencyPreparer func(context.Context, string) (dependencies.EnsureResult, error)

// WarmOptions configures the warm orchestration service.
type WarmOptions struct {
	Lifecycle *Service
	Reader    WarmStateReader
	Locker    WarmLocker
	Worktree  WarmWorkspaceService

	PoolMaintenanceLockPath string
	WarmLockPath            string
	WorktreeHeads           WarmHeadReader
	TargetHead              WarmTargetHead
	ValidateDependencies    WarmDependencyValidator
	Prepare                 WarmDependencyPreparer

	// WorkspacePath returns a new destination for slot number index. The
	// factory is called only for missing capacity while both maintenance locks
	// are held.
	WorkspacePath func(context.Context, int) (string, error)
}

// WarmService ensures a requested number of valid, available prepared slots.
type WarmService struct {
	lifecycle            *Service
	reader               WarmStateReader
	locker               WarmLocker
	worktree             WarmWorkspaceService
	poolMaintenanceLock  string
	warmLock             string
	worktreeHeads        WarmHeadReader
	targetHead           WarmTargetHead
	validateDependencies WarmDependencyValidator
	prepare              WarmDependencyPreparer
	workspacePath        func(context.Context, int) (string, error)
}

// NewWarmService constructs a warm-pool orchestrator.
func NewWarmService(options WarmOptions) *WarmService {
	if options.Lifecycle == nil {
		panic("lifecycle: nil warm lifecycle")
	}
	return &WarmService{
		lifecycle:            options.Lifecycle,
		reader:               options.Reader,
		locker:               options.Locker,
		worktree:             options.Worktree,
		poolMaintenanceLock:  options.PoolMaintenanceLockPath,
		warmLock:             options.WarmLockPath,
		worktreeHeads:        options.WorktreeHeads,
		targetHead:           options.TargetHead,
		validateDependencies: options.ValidateDependencies,
		prepare:              options.Prepare,
		workspacePath:        options.WorkspacePath,
	}
}

// WarmInput describes one pool warming request.
type WarmInput struct {
	Count      int
	StartPoint string
}

// WarmResult is the stable warm command result. Created contains only
// successfully published available workspace paths.
type WarmResult struct {
	Status    string   `json:"status"`
	Requested int      `json:"requested"`
	Available int      `json:"available"`
	Created   []string `json:"created"`
}

var (
	errWarmLocksNotConfigured  = errors.New("warm maintenance locks are not configured")
	errWarmReaderNotConfigured = errors.New("warm state reader is not configured")
)

// Warm counts only integrity-valid available capacity while holding the
// repository-wide pool-maintenance and warm locks. Missing slots are created
// as preparing records, prepared, locked in Git, and published as available;
// failures remain failed records for explicit recovery/GC.
func (service *WarmService) Warm(ctx context.Context, input WarmInput) (WarmResult, error) {
	if service == nil || service.lifecycle == nil {
		return WarmResult{}, errors.New("warm service is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if input.Count < 1 {
		return WarmResult{}, errors.New("count must be a positive integer")
	}
	if input.StartPoint == "" {
		return WarmResult{}, errors.New("start point must not be empty")
	}
	if service.reader == nil {
		return WarmResult{}, errWarmReaderNotConfigured
	}
	if service.locker == nil || service.poolMaintenanceLock == "" || service.warmLock == "" {
		return WarmResult{}, errWarmLocksNotConfigured
	}
	if service.worktree == nil || service.worktreeHeads == nil || service.targetHead == nil || service.validateDependencies == nil || service.prepare == nil || service.workspacePath == nil {
		return WarmResult{}, errors.New("warm service is not fully configured")
	}
	var result WarmResult
	err := service.locker.With(ctx, service.poolMaintenanceLock, func() error {
		return service.locker.With(ctx, service.warmLock, func() error {
			return service.warmLocked(ctx, input, &result)
		})
	})
	if err != nil {
		return WarmResult{}, err
	}
	return result, nil
}

func (service *WarmService) warmLocked(ctx context.Context, input WarmInput, result *WarmResult) error {
	current, err := service.reader.Read(ctx)
	if err != nil {
		return err
	}
	if current == nil {
		return errors.New("warm state reader returned nil state")
	}
	target, err := service.targetHead(ctx, input.StartPoint)
	if err != nil {
		return err
	}
	if target == "" {
		return errors.New("warm target head must not be empty")
	}
	heads, err := service.worktreeHeads(ctx)
	if err != nil {
		return err
	}
	available, err := service.countValidAvailable(ctx, current, heads, target)
	if err != nil {
		return err
	}
	created := make([]string, 0)
	for index := available; index < input.Count; index++ {
		path, createErr := service.createSlot(ctx, index, input.StartPoint)
		if createErr != nil {
			return createErr
		}
		created = append(created, path)
	}
	*result = WarmResult{
		Status:    "warmed",
		Requested: input.Count,
		Available: available + len(created),
		Created:   created,
	}
	return nil
}

func (service *WarmService) countValidAvailable(ctx context.Context, current *state.State, heads map[string]string, target string) (int, error) {
	paths := make([]string, 0, len(current.Workspaces))
	for _, workspace := range current.Workspaces {
		if workspace.Lifecycle != state.LifecycleAvailable || workspace.OperationID != nil {
			continue
		}
		paths = append(paths, workspace.Path)
	}
	sort.Strings(paths)
	count := 0
	for _, path := range paths {
		key, err := state.TreeKey(path)
		if err != nil {
			return 0, err
		}
		head, ok := lookupHead(heads, path, key)
		if !ok || head != target {
			continue
		}
		tree, ok := current.Trees[key]
		if !ok || !dependencies.ProjectionIntegrityValid(path, tree.Projections, tree.ProjectionFingerprint) {
			continue
		}
		valid, err := service.validateDependencies(ctx, path, tree)
		if err != nil {
			return 0, err
		}
		if valid {
			count++
		}
	}
	return count, nil
}

func lookupHead(heads map[string]string, path, key string) (string, bool) {
	if head, ok := heads[path]; ok {
		return head, true
	}
	head, ok := heads[key]
	return head, ok
}

func (service *WarmService) createSlot(ctx context.Context, index int, startPoint string) (string, error) {
	path, err := service.workspacePath(ctx, index)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve warm workspace path: %w", err)
	}
	preparing, err := service.lifecycle.BeginPreparation(ctx, path, "(warm)")
	if err != nil {
		return "", err
	}
	if preparing.OperationID == nil {
		return "", errors.New("warm preparation has no operation fence")
	}
	operationID := *preparing.OperationID
	if err := service.worktree.Create(ctx, preparing.Path, "(warm)", startPoint, true); err != nil {
		return "", service.markWarmFailed(ctx, preparing.Path, operationID, err)
	}
	if err := service.worktree.Lock(ctx, preparing.Path); err != nil {
		return "", service.markWarmFailed(ctx, preparing.Path, operationID, err)
	}
	if _, err := service.prepare(ctx, preparing.Path); err != nil {
		return "", service.markWarmFailed(ctx, preparing.Path, operationID, err)
	}
	if _, err := service.lifecycle.MarkAvailable(ctx, preparing.Path, operationID); err != nil {
		return "", service.markWarmFailed(ctx, preparing.Path, operationID, err)
	}
	return preparing.Path, nil
}

func (service *WarmService) markWarmFailed(ctx context.Context, path, operationID string, cause error) error {
	if cause == nil {
		cause = errors.New("warm preparation failed")
	}
	_, markErr := service.lifecycle.MarkFailed(ctx, path, operationID, cause.Error())
	return errors.Join(cause, markErr)
}
