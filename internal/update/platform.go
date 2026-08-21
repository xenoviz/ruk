package update

import (
	"path/filepath"
	"runtime"
)

// runtime helpers are variables to keep platform naming testable without
// requiring platform-specific files in this foundational package.
var runtimeGOOS = func() string { return runtime.GOOS }

func updatePlatformOS(platform Platform) string {
	if platform.OS != "" {
		return platform.OS
	}
	return runtimeGOOS()
}

var runtimeGOARCH = func() string { return runtime.GOARCH }

// RuntimePlatform identifies the native standalone release asset without
// spawning liveness or shell helpers. Static Go binaries run on musl, but the
// release asset name remains part of Ruk's public update contract.
func RuntimePlatform() Platform {
	osName := runtimeGOOS()
	return Platform{OS: osName, Architecture: runtimeGOARCH(), Musl: detectMuslWith(osName, filepath.Glob)}
}

func detectMuslWith(osName string, glob func(string) ([]string, error)) bool {
	if osName != "linux" || glob == nil {
		return false
	}
	matches, err := glob("/lib/ld-musl-*.so.1")
	return err == nil && len(matches) != 0
}
