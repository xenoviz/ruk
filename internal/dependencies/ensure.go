package dependencies

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/git"
	"github.com/xenoviz/ruk/internal/state"
)

// StateStore is the durable state seam used by dependency preparation. The
// interface deliberately includes both read and update so an orchestrator can
// be tested without creating a repository state file.
type StateStore interface {
	Read(context.Context) (*state.State, error)
	Update(context.Context, func(*state.State) error) error
}

// DirectoryLocker serializes preparation of one workspace. It matches the
// lock package's small callback interface and is intentionally injected so
// callers can use an in-memory lock in deterministic tests.
type DirectoryLocker interface {
	With(context.Context, string, func() error) error
}

// DependencyFileLister returns repository-relative paths used for dependency
// fingerprinting. A lister is called again after installation when supplied,
// matching the TypeScript implementation's post-install source rescan.
type DependencyFileLister func(context.Context, string) ([]string, error)

// CurrentBranchReader supplies the branch recorded with preparation metadata.
type CurrentBranchReader func(context.Context, string) (string, error)

// InstallerBackend is the command runner seam used by EnsureDependencies.
// Installer satisfies this interface; callers can provide a narrow fake for
// orchestration tests without launching a package manager.
type InstallerBackend interface {
	Prepare(context.Context, string, PackageManager) (InstallResult, error)
}

// EnsureInput describes one complete dependency preparation operation.
// Files are used when ListFiles is nil. Manager is normally the effective
// manager selected by config.DetectPackageManager, with Version and Runtime
// supplied by the caller when available for native dependency identity.
type EnsureInput struct {
	Repository git.Repository
	Manager    PackageManager
	Runtime    RuntimeIdentity
	Files      []string
	ListFiles  DependencyFileLister

	Store           StateStore
	Locker          DirectoryLocker
	Installer       InstallerBackend
	CurrentBranch   CurrentBranchReader
	BeforePrepare   func() error
	Now             func() time.Time
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	MachineReadable bool
}

// EnsureDependenciesInput is retained as a descriptive alias for callers
// migrating from the TypeScript object-shaped API.
type EnsureDependenciesInput = EnsureInput

// EnsureResult is the stable machine-readable result of one preparation.
// Reused and AlreadyAttached are both true only when no installer ran and the
// recorded projection passed its integrity check.
type EnsureResult struct {
	Fingerprint     string `json:"fingerprint"`
	Mode            string `json:"mode"`
	Reused          bool   `json:"reused"`
	AlreadyAttached bool   `json:"alreadyAttached"`
}

// EnsureDependenciesResult is a descriptive alias matching the TypeScript
// result name.
type EnsureDependenciesResult = EnsureResult

// EnsureDependencies fingerprints the current inputs, reuses an intact
// projection when possible, otherwise removes the old projection, runs the
// selected installer, validates the resulting projection, and publishes its
// metadata atomically through Store. Preparation is serialized by a lock
// derived from the repository's common Git directory and workspace path.
func EnsureDependencies(ctx context.Context, input EnsureInput) (EnsureResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return EnsureResult{}, err
	}
	if input.Repository.Root == "" {
		return EnsureResult{}, errors.New("repository root must not be empty")
	}
	if input.Repository.CommonDir == "" {
		return EnsureResult{}, errors.New("Git common directory must not be empty")
	}
	root, err := filepath.Abs(filepath.Clean(input.Repository.Root))
	if err != nil {
		return EnsureResult{}, fmt.Errorf("resolve dependency root: %w", err)
	}
	input.Repository.Root = root

	store := input.Store
	locker := input.Locker
	if locker == nil {
		locker, err = newNativeDirectoryLocker(ctx)
		if err != nil {
			return EnsureResult{}, err
		}
	}
	if store == nil {
		store = state.NewStore(input.Repository.CommonDir, locker)
	}
	installer := input.Installer
	if installer == nil {
		installer = Installer{
			Stdin: input.Stdin, Stdout: input.Stdout, Stderr: input.Stderr,
			InheritStdio: !input.MachineReadable,
		}
	}
	branch := input.CurrentBranch
	if branch == nil {
		branch = func(ctx context.Context, root string) (string, error) {
			return git.CurrentBranch(ctx, root, nil)
		}
	}
	now := input.Now
	if now == nil {
		now = time.Now
	}
	key, err := state.TreeKey(root)
	if err != nil {
		return EnsureResult{}, err
	}
	lockPath := filepath.Join(input.Repository.CommonDir, "ruk", "locks", "workspace-"+key+".lock")

	started := now()
	var result EnsureResult
	err = locker.With(ctx, lockPath, func() error {
		if input.BeforePrepare != nil {
			if err := input.BeforePrepare(); err != nil {
				return err
			}
		}
		result, err = ensureLocked(ctx, input, root, key, store, installer, branch, now)
		return err
	})
	if err == nil {
		metricErr := error(nil)
		if result.AlreadyAttached {
			metricErr = recordMetric(ctx, store, "skipped", elapsedMilliseconds(now().Sub(started)))
		} else if metricErr == nil {
			metricErr = recordMetric(ctx, store, "prepared", elapsedMilliseconds(now().Sub(started)))
		}
		if metricErr != nil {
			return EnsureResult{}, metricErr
		}
		return result, nil
	}
	// Preparation metrics are observational. A state-write failure here must
	// never replace or obscure the dependency failure that callers need to
	// classify and report.
	_ = recordMetric(ctx, store, "failed", elapsedMilliseconds(now().Sub(started)))
	return EnsureResult{}, err
}

// Ensure is a concise alias for EnsureDependencies.
func Ensure(ctx context.Context, input EnsureInput) (EnsureResult, error) {
	return EnsureDependencies(ctx, input)
}

func ensureLocked(ctx context.Context, input EnsureInput, root, key string, store StateStore, installer InstallerBackend, branch CurrentBranchReader, now func() time.Time) (EnsureResult, error) {
	files, err := dependencyFiles(ctx, root, input)
	if err != nil {
		return EnsureResult{}, err
	}
	details, err := DependencyFingerprint(SourceFingerprintInput{
		Root: root, Files: files, Manager: input.Manager, Runtime: input.Runtime,
	})
	if err != nil {
		return EnsureResult{}, err
	}
	if details.Manager.DependencyMode == "shared" {
		if err := AssertSharedBackendSupported(details.Manager.Name, details.Manager.Version); err != nil {
			return EnsureResult{}, err
		}
	}

	snapshot, err := store.Read(ctx)
	if err != nil {
		return EnsureResult{}, err
	}
	if snapshot == nil {
		return EnsureResult{}, errors.New("dependency state store returned nil state")
	}
	current, exists := snapshot.Trees[key]
	if exists && current.Fingerprint == details.Fingerprint && ProjectionIntegrityValid(root, current.Projections, current.ProjectionFingerprint) {
		return EnsureResult{
			Fingerprint:     details.Fingerprint,
			Mode:            current.Mode,
			Reused:          true,
			AlreadyAttached: true,
		}, nil
	}
	if exists {
		if err := removeDependencyProjections(root, current.Projections); err != nil {
			return EnsureResult{}, err
		}
	}

	installer = configureInstallerProcessTracking(installer, store, key, root, snapshot, now)
	if _, err := installer.Prepare(ctx, root, details.Manager); err != nil {
		return EnsureResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return EnsureResult{}, err
	}
	files, err = dependencyFiles(ctx, root, input)
	if err != nil {
		return EnsureResult{}, err
	}
	details, err = DependencyFingerprint(SourceFingerprintInput{
		Root: root, Files: files, Manager: input.Manager, Runtime: input.Runtime,
	})
	if err != nil {
		return EnsureResult{}, err
	}
	projections, err := projectionPaths(root, details.Files)
	if err != nil {
		return EnsureResult{}, err
	}
	if len(projections) == 0 {
		return EnsureResult{}, errors.New("Dependency installation completed without creating a node_modules projection")
	}
	projectionFingerprint, err := ProjectionFingerprint(root, projections)
	if err != nil {
		return EnsureResult{}, err
	}
	currentBranch, err := branch(ctx, root)
	if err != nil {
		return EnsureResult{}, err
	}
	mode := dependencyMode(details.Manager)
	updated := state.TreeRecord{
		Path:                  root,
		Fingerprint:           details.Fingerprint,
		ProjectionFingerprint: projectionFingerprint,
		Mode:                  mode,
		Projections:           append([]string(nil), projections...),
		Branch:                currentBranch,
		UpdatedAt:             now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if err := store.Update(ctx, func(current *state.State) error {
		if current == nil {
			return errors.New("dependency state store returned nil state")
		}
		if current.Trees == nil {
			current.Trees = map[string]state.TreeRecord{}
		}
		current.Trees[key] = updated
		return nil
	}); err != nil {
		return EnsureResult{}, err
	}
	return EnsureResult{Fingerprint: details.Fingerprint, Mode: mode}, nil
}

func configureInstallerProcessTracking(installer InstallerBackend, store StateStore, key, root string, snapshot *state.State, now func() time.Time) InstallerBackend {
	if installer == nil || store == nil || snapshot == nil {
		return installer
	}
	workspace, ok := snapshot.Workspaces[key]
	if !ok || workspace.Path != root {
		return installer
	}
	var assignmentID, operationID string
	switch workspace.Lifecycle {
	case state.LifecycleAssigned:
		if workspace.Assignment == nil {
			return installer
		}
		assignmentID = workspace.Assignment.ID
	case state.LifecyclePreparing:
		if workspace.OperationID == nil || *workspace.OperationID == "" {
			return installer
		}
		operationID = *workspace.OperationID
	default:
		return installer
	}
	register := func(ctx context.Context, record state.TrackedProcessRecord) error {
		return store.Update(ctx, func(current *state.State) error {
			if current == nil {
				return errors.New("dependency state store returned nil state")
			}
			workspace, exists := current.Workspaces[key]
			if !exists || workspace.Path != root {
				return errors.New("workspace is unavailable for installer process tracking")
			}
			if assignmentID != "" {
				if workspace.Lifecycle != state.LifecycleAssigned || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
					return errors.New("assigned workspace fence changed during installer process tracking")
				}
			} else if workspace.Lifecycle != state.LifecyclePreparing || workspace.OperationID == nil || *workspace.OperationID != operationID {
				return errors.New("preparing workspace fence changed during installer process tracking")
			}
			for _, tracked := range workspace.Processes {
				if tracked.PID == record.PID {
					if tracked.StartedAt == record.StartedAt {
						return nil
					}
					return fmt.Errorf("installer process %d is already tracked", record.PID)
				}
			}
			workspace.Processes = append(workspace.Processes, record)
			workspace.UpdatedAt = nextWorkspaceUpdatedAt(workspace.UpdatedAt, now)
			current.Workspaces[key] = workspace
			return nil
		})
	}
	remove := func(ctx context.Context, record state.TrackedProcessRecord) error {
		return store.Update(ctx, func(current *state.State) error {
			if current == nil {
				return errors.New("dependency state store returned nil state")
			}
			workspace, exists := current.Workspaces[key]
			if !exists || workspace.Path != root {
				return errors.New("workspace is unavailable for installer process cleanup")
			}
			if assignmentID != "" {
				if workspace.Lifecycle != state.LifecycleAssigned || workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
					return errors.New("assigned workspace fence changed during installer process cleanup")
				}
			} else if workspace.Lifecycle != state.LifecyclePreparing || workspace.OperationID == nil || *workspace.OperationID != operationID {
				return errors.New("preparing workspace fence changed during installer process cleanup")
			}
			for index, tracked := range workspace.Processes {
				if tracked.PID == record.PID && tracked.StartedAt == record.StartedAt {
					workspace.Processes = append(workspace.Processes[:index], workspace.Processes[index+1:]...)
					workspace.UpdatedAt = nextWorkspaceUpdatedAt(workspace.UpdatedAt, now)
					current.Workspaces[key] = workspace
					return nil
				}
			}
			return fmt.Errorf("installer process %d with identity %s is not tracked", record.PID, record.StartedAt)
		})
	}

	switch configured := installer.(type) {
	case Installer:
		configured.RegisterProcess = register
		configured.RemoveProcess = remove
		return configured
	case *Installer:
		if configured == nil {
			return installer
		}
		copy := *configured
		copy.RegisterProcess = register
		copy.RemoveProcess = remove
		return &copy
	default:
		return installer
	}
}

func nextWorkspaceUpdatedAt(previous string, now func() time.Time) string {
	observed := time.Now().UTC().Truncate(time.Millisecond)
	if now != nil {
		observed = now().UTC().Truncate(time.Millisecond)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, previous); err == nil && !observed.After(parsed) {
		observed = parsed.UTC().Truncate(time.Millisecond).Add(time.Millisecond)
	}
	return observed.Format("2006-01-02T15:04:05.000Z")
}

func dependencyFiles(ctx context.Context, root string, input EnsureInput) ([]string, error) {
	if input.ListFiles != nil {
		files, err := input.ListFiles(ctx, root)
		if err != nil {
			return nil, err
		}
		return files, nil
	}
	if input.Files == nil {
		return nil, errors.New("dependency repository file listing is required")
	}
	return append([]string(nil), input.Files...), nil
}

func dependencyMode(manager PackageManager) string {
	if manager.DependencyMode == "shared" {
		return manager.Name + "-global-store"
	}
	return "managed-install"
}

func projectionPaths(root string, files []string) ([]string, error) {
	candidates := map[string]struct{}{"node_modules": {}}
	for _, file := range files {
		normalized := strings.ReplaceAll(file, `\`, "/")
		if normalized == "package.json" || strings.HasSuffix(normalized, "/package.json") {
			directory := path.Dir(normalized)
			if directory == "." {
				directory = ""
			}
			candidates[path.Join(directory, "node_modules")] = struct{}{}
		}
	}
	result := make([]string, 0, len(candidates))
	for relative := range candidates {
		target, err := safeProjectionPath(root, relative)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(target); err == nil {
			result = append(result, filepath.ToSlash(filepath.Clean(relative)))
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect dependency projection %q: %w", relative, err)
		}
	}
	sort.Strings(result)
	return result, nil
}

func removeDependencyProjections(root string, projections []string) error {
	for _, relative := range projections {
		target, err := safeProjectionPath(root, relative)
		if err != nil {
			return err
		}
		resolvedRoot, _ := filepath.Abs(filepath.Clean(root))
		for ancestor := filepath.Dir(target); ancestor != resolvedRoot; ancestor = filepath.Dir(ancestor) {
			info, statErr := os.Lstat(ancestor)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return fmt.Errorf("inspect dependency projection ancestor %q: %w", relative, statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("Dependency projection has a symlinked ancestor: %s", relative)
			}
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove dependency projection %q: %w", relative, err)
		}
	}
	return nil
}

func safeProjectionPath(root, relative string) (string, error) {
	resolvedRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve dependency root: %w", err)
	}
	if relative == "" {
		return "", errors.New("dependency projection path cannot be empty")
	}
	normalized := strings.ReplaceAll(relative, `\`, "/")
	if filepath.IsAbs(relative) || strings.HasPrefix(normalized, "/") || (len(normalized) >= 2 && normalized[1] == ':') {
		return "", fmt.Errorf("dependency projection must stay inside the workspace: %s", relative)
	}
	target := filepath.Clean(filepath.Join(resolvedRoot, filepath.FromSlash(normalized)))
	if target == resolvedRoot || !strings.HasPrefix(target, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("dependency projection must stay inside the workspace: %s", relative)
	}
	return target, nil
}

func recordMetric(ctx context.Context, store StateStore, kind string, duration int64) error {
	if store == nil || kind == "" {
		return nil
	}
	return store.Update(ctx, func(current *state.State) error {
		if current == nil {
			return errors.New("dependency state store returned nil state")
		}
		if duration < 0 {
			duration = 0
		}
		current.Metrics.LastPreparationMS = &duration
		switch kind {
		case "prepared":
			current.Metrics.Preparations++
			current.Metrics.TotalPreparationMS += duration
		case "skipped":
			current.Metrics.PreparationSkips++
		case "failed":
			current.Metrics.PreparationFailures++
		default:
			return fmt.Errorf("unknown dependency preparation metric %q", kind)
		}
		return nil
	})
}

func elapsedMilliseconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	milliseconds := duration / time.Millisecond
	if duration%time.Millisecond >= 500*time.Microsecond {
		milliseconds++
	}
	return int64(milliseconds)
}
