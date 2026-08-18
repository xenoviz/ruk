package dependencies

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xenoviz/ruk/internal/config"
)

// UnknownManagerVersion is used when a manager's version is not relevant to
// the selected dependency backend. In particular, Ruk must not execute an
// arbitrary custom managed installer merely to fingerprint it.
const UnknownManagerVersion = "unknown"

// ManagerResolver turns the config package's package-manager selection into
// the dependency package's executable manager. Runner is used only for the
// bounded version probe required by a shared Bun or pnpm backend. A nil Runner
// uses OSCommandRunner.
type ManagerResolver struct {
	Runner          CommandRunner
	DiagnosticLimit int
}

// NewManagerResolver constructs a resolver with an injected process seam.
func NewManagerResolver(runner CommandRunner) ManagerResolver {
	return ManagerResolver{Runner: runner}
}

// ResolvePackageManager selects and converts the repository's package
// manager. The optional runner is a test seam; omitting it uses the operating
// system runner. Shared Bun and pnpm selections are version-probed and
// validated before being returned. Managed selections, including custom
// commands, never run an installer command during resolution.
func ResolvePackageManager(ctx context.Context, root string, cfg config.Config, runners ...CommandRunner) (PackageManager, error) {
	var runner CommandRunner
	if len(runners) > 0 {
		runner = runners[0]
	}
	return (ManagerResolver{Runner: runner}).Resolve(ctx, root, cfg)
}

// DetectPackageManager is a compatibility-shaped alias for callers that want
// the config detection and dependency conversion in one operation.
func DetectPackageManager(ctx context.Context, root string, cfg config.Config, runners ...CommandRunner) (PackageManager, error) {
	return ResolvePackageManager(ctx, root, cfg, runners...)
}

// Resolve performs package-manager selection and conversion.
func (resolver ManagerResolver) Resolve(ctx context.Context, root string, cfg config.Config) (PackageManager, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PackageManager{}, err
	}

	selected, err := config.DetectPackageManager(root, cfg)
	if err != nil {
		return PackageManager{}, err
	}
	manager := PackageManager{
		Name:           selected.Name,
		Command:        append([]string(nil), selected.Command...),
		DependencyMode: string(selected.DependencyMode),
		Version:        UnknownManagerVersion,
	}
	if manager.DependencyMode == "" {
		manager.DependencyMode = string(config.Managed)
	}

	// Probe only standard package managers. Custom commands may be wrappers,
	// scripts, or arbitrary user programs and must never be executed merely to
	// fingerprint a projection.
	if !isStandardManagerSelection(manager) {
		if manager.DependencyMode != string(config.Shared) {
			return manager, nil
		}
		return PackageManager{}, AssertSharedBackendSupported(manager.Name, manager.Version)
	}
	version, probeErr := resolver.probeVersion(ctx, root, manager.Name, manager.Command)
	if probeErr != nil {
		return PackageManager{}, probeErr
	}
	manager.Version = version
	if manager.DependencyMode != string(config.Shared) {
		return manager, nil
	}
	if err := AssertSharedBackendSupported(manager.Name, manager.Version); err != nil {
		return PackageManager{}, err
	}
	return manager, nil
}

func isStandardPackageManager(name string) bool {
	switch name {
	case "bun", "npm", "pnpm", "yarn":
		return true
	default:
		return false
	}
}

func isStandardManagerSelection(manager PackageManager) bool {
	return isStandardPackageManager(manager.Name) && len(manager.Command) > 0 && managerCommandName(manager.Command[0]) == manager.Name
}

func managerCommandName(command string) string {
	name := filepath.Base(strings.TrimSpace(command))
	if len(name) >= len(".exe") && strings.EqualFold(name[len(name)-len(".exe"):], ".exe") {
		name = name[:len(name)-len(".exe")]
	}
	return name
}

func (resolver ManagerResolver) probeVersion(ctx context.Context, root, name string, command []string) (string, error) {
	if len(command) == 0 || command[0] == "" {
		return "", errors.New("Package manager command cannot be empty")
	}
	runner := resolver.Runner
	if runner == nil {
		runner = OSCommandRunner
	}
	result, err := runner(ctx, CommandRequest{
		Command: command[0],
		Args:    []string{"--version"},
		Dir:     root,
	})
	limit, limitErr := resolver.limit()
	if limitErr != nil {
		return "", limitErr
	}
	stdout := tail(result.Stdout, limit)
	stderr := tail(result.Stderr, limit)
	if err != nil {
		return "", fmt.Errorf("Could not inspect %s for dependency preparation: %w", name, err)
	}
	if result.ExitCode != 0 {
		// A failed --version invocation follows the TypeScript contract: retain
		// an unknown version so shared-backend validation emits the stable
		// minimum-version error. The bounded streams are intentionally discarded
		// here; version output is not an installer diagnostic surface.
		return UnknownManagerVersion, nil
	}
	version, ok := parseManagerVersion(stdout)
	if !ok {
		version, ok = parseManagerVersion(stderr)
	}
	if !ok {
		return UnknownManagerVersion, nil
	}
	return version, nil
}

func (resolver ManagerResolver) limit() (int, error) {
	limit := resolver.DiagnosticLimit
	if limit == 0 {
		return defaultDiagnosticLimit, nil
	}
	if limit < 0 {
		return 0, errors.New("dependency manager diagnostic limit must not be negative")
	}
	return limit, nil
}

func parseManagerVersion(value string) (string, bool) {
	version, ok := numericVersion(value)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2]), true
}
