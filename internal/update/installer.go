package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

func InstallerFromPath(entrypoint string) Installer {
	normalized := strings.ToLower(strings.ReplaceAll(entrypoint, "\\", "/"))
	switch {
	case strings.Contains(normalized, "/.bun/install/global/"):
		return InstallerBun
	case strings.Contains(normalized, "/pnpm/global/") || strings.Contains(normalized, "/.pnpm/"):
		return InstallerPNPM
	case strings.Contains(normalized, "/yarn/global/"):
		return InstallerYarn
	default:
		return InstallerNPM
	}
}

// DetectInstaller reads the durable marker written by the npm distribution.
// Older package installations fall back to path detection for compatibility.
func DetectInstaller(entrypoint string) (Installer, error) {
	if strings.TrimSpace(entrypoint) == "" {
		return InstallerNPM, nil
	}
	markerPath := entrypoint + ".ruk-distribution"
	bytes, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return InstallerFromPath(entrypoint), nil
	}
	if err != nil {
		return "", fmt.Errorf("read package distribution marker: %w", err)
	}
	var marker struct {
		SchemaVersion int    `json:"schemaVersion"`
		Distribution  string `json:"distribution"`
		Installer     string `json:"installer"`
	}
	if json.Unmarshal(bytes, &marker) != nil || marker.SchemaVersion != 1 || marker.Distribution != string(DistributionPackage) {
		return "", errors.New("package distribution marker is invalid")
	}
	return ParseInstaller(marker.Installer)
}

func ParseInstaller(value string) (Installer, error) {
	installer := Installer(value)
	switch installer {
	case InstallerBun, InstallerNPM, InstallerPNPM, InstallerYarn:
		return installer, nil
	default:
		return "", fmt.Errorf("Unsupported update installer %s; expected bun, npm, pnpm, or yarn", value)
	}
}

func InstallerCommand(installer Installer, version string) (string, []string, error) {
	specification := PackageName + "@" + version
	switch installer {
	case InstallerBun:
		// Bun does not run dependency lifecycle scripts during installs unless
		// the package is explicitly trusted. The root package's postinstall is
		// what installs the native launcher, so trust only this exact package
		// operation rather than enabling arbitrary scripts globally.
		return "bun", []string{"add", "--global", "--trust", specification}, nil
	case InstallerNPM, "":
		return "npm", []string{"install", "--global", specification}, nil
	case InstallerPNPM:
		return "pnpm", []string{"add", "--global", specification}, nil
	case InstallerYarn:
		return "yarn", []string{"global", "add", specification}, nil
	default:
		return "", nil, fmt.Errorf("Unsupported update installer %s; expected bun, npm, pnpm, or yarn", installer)
	}
}

func ExecutableAsset(platform Platform) (string, error) {
	architecture := platform.Architecture
	if architecture == "x64" {
		architecture = "amd64"
	}
	switch {
	case platform.OS == "darwin" && architecture == "amd64":
		return "ruk-macos-x64", nil
	case platform.OS == "darwin" && architecture == "arm64":
		return "ruk-macos-arm64", nil
	case platform.OS == "windows" && architecture == "amd64":
		return "ruk-windows-x64.exe", nil
	case platform.OS == "windows" && architecture == "arm64":
		return "ruk-windows-arm64.exe", nil
	case platform.OS == "linux" && architecture == "arm64" && !platform.Musl:
		return "ruk-linux-arm64", nil
	case platform.OS == "linux" && architecture == "amd64":
		if platform.Musl {
			return "ruk-linux-x64-musl", nil
		}
		return "ruk-linux-x64", nil
	default:
		libc := ""
		if platform.Musl {
			libc = "/musl"
		}
		return "", fmt.Errorf("Standalone updates are not available for %s/%s%s", platform.OS, architecture, libc)
	}
}

// AssetName is a convenience wrapper for callers that use GOOS-style
// platform strings rather than a Platform value.
func AssetName(platform, architecture string, musl bool) (string, error) {
	return ExecutableAsset(Platform{OS: platform, Architecture: architecture, Musl: musl})
}
