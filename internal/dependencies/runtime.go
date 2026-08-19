package dependencies

import (
	"context"
	"fmt"
	"runtime"
	"strings"
)

// ResolveRuntimeIdentity discovers only standard runtime executables. Custom
// install commands are intentionally represented by stable unknown values and
// are never executed for fingerprinting.
func ResolveRuntimeIdentity(ctx context.Context, root string, manager PackageManager, runners ...CommandRunner) (RuntimeIdentity, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	identity := RuntimeIdentity{Platform: runtime.GOOS, Architecture: runtime.GOARCH, Runtime: "unknown", Version: "unknown", NativeABI: "unknown"}
	if !isStandardManagerSelection(manager) {
		return identity, nil
	}
	runner := CommandRunner(OSCommandRunner)
	if len(runners) > 0 && runners[0] != nil {
		runner = runners[0]
	}
	runtimeName := "node"
	if manager.Name == "bun" {
		runtimeName = "bun"
	}
	identity.Runtime = runtimeName
	identity.Version = manager.Version
	if runtimeName == "node" {
		result, err := runner(ctx, CommandRequest{Command: runtimeName, Args: []string{"--version"}, Dir: root})
		if err != nil {
			return RuntimeIdentity{}, fmt.Errorf("inspect %s version: %w", runtimeName, err)
		}
		if result.ExitCode != 0 {
			return RuntimeIdentity{}, fmt.Errorf("inspect %s version exited with code %d", runtimeName, result.ExitCode)
		}
		version, ok := parseManagerVersion(result.Stdout)
		if !ok {
			version, ok = parseManagerVersion(result.Stderr)
		}
		if !ok {
			return RuntimeIdentity{}, fmt.Errorf("inspect %s version returned no usable value", runtimeName)
		}
		identity.Version = version
	}
	if identity.Version == "" || identity.Version == UnknownManagerVersion {
		return RuntimeIdentity{}, fmt.Errorf("%s runtime version is unavailable", runtimeName)
	}
	abiArgs := []string{"-p", "process.versions.modules"}
	if runtimeName == "bun" {
		abiArgs = []string{"-e", "console.log(process.versions.modules)"}
	}
	result, err := runner(ctx, CommandRequest{Command: runtimeName, Args: abiArgs, Dir: root})
	if err != nil {
		return RuntimeIdentity{}, fmt.Errorf("inspect %s native ABI: %w", runtimeName, err)
	}
	if result.ExitCode != 0 {
		return RuntimeIdentity{}, fmt.Errorf("inspect %s native ABI exited with code %d", runtimeName, result.ExitCode)
	}
	abi := strings.TrimSpace(result.Stdout)
	if abi == "" {
		abi = strings.TrimSpace(result.Stderr)
	}
	if (abi == "" || abi == "undefined" || abi == "null") && runtimeName == "bun" {
		// Bun does not expose a Node modules ABI on every supported release;
		// its own version is still a safe native-runtime invalidation fence.
		abi = "bun-" + identity.Version
	}
	if abi == "" || abi == "undefined" || abi == "null" {
		return RuntimeIdentity{}, fmt.Errorf("inspect %s native ABI returned no value", runtimeName)
	}
	identity.NativeABI = abi
	return identity, nil
}
