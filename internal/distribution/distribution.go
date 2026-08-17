// Package distribution defines the native artifacts shipped by Ruk.
//
// The package and standalone distributions have different update owners, so
// callers must carry an explicit Distribution marker.  Platform mappings are
// deliberately bounded to the targets published by the release workflow.
package distribution

import (
	"errors"
	"fmt"
)

// Distribution identifies who owns an installed Ruk executable's updates.
// It must not be inferred from the executable path: package installations
// delegate to their package manager, while standalone installations replace
// the executable from a verified release asset.
type Distribution string

const (
	DistributionPackage    Distribution = "package"
	DistributionStandalone Distribution = "standalone"

	// Package and Standalone are concise aliases for composition code.
	Package    = DistributionPackage
	Standalone = DistributionStandalone
)

// Valid reports whether d is one of Ruk's explicit distribution markers.
func (d Distribution) Valid() bool {
	return d == DistributionPackage || d == DistributionStandalone
}

// ErrUnsupportedTarget indicates that no native artifact is published for a
// requested platform. Callers can use errors.Is to classify this failure.
var ErrUnsupportedTarget = errors.New("unsupported Ruk distribution target")

// Target is a Go platform target. GOOS and GOARCH use the values returned by
// runtime.GOOS and runtime.GOARCH. Musl is meaningful only for Linux amd64;
// Linux arm64 musl is intentionally not included because no such release
// asset currently exists.
type Target struct {
	GOOS   string
	GOARCH string
	Musl   bool
}

// Mapping names every artifact associated with a supported native target.
// AssetName is the canonical GitHub Release asset; PackageName is the npm
// optional package selected by the metadata package; ExecutableName is the
// installed executable filename.
type Mapping struct {
	AssetName      string
	PackageName    string
	ExecutableName string
}

// supportedTargets is the single source of truth for the release matrix.
var supportedTargets = []struct {
	target  Target
	mapping Mapping
}{
	{Target{"linux", "amd64", false}, Mapping{"ruk-linux-x64", "@xenoviz/ruk-linux-x64", "ruk"}},
	{Target{"linux", "arm64", false}, Mapping{"ruk-linux-arm64", "@xenoviz/ruk-linux-arm64", "ruk"}},
	{Target{"linux", "amd64", true}, Mapping{"ruk-linux-x64-musl", "@xenoviz/ruk-linux-x64-musl", "ruk"}},
	{Target{"darwin", "amd64", false}, Mapping{"ruk-macos-x64", "@xenoviz/ruk-darwin-x64", "ruk"}},
	{Target{"darwin", "arm64", false}, Mapping{"ruk-macos-arm64", "@xenoviz/ruk-darwin-arm64", "ruk"}},
	{Target{"windows", "amd64", false}, Mapping{"ruk-windows-x64.exe", "@xenoviz/ruk-windows-x64", "ruk.exe"}},
	{Target{"windows", "arm64", false}, Mapping{"ruk-windows-arm64.exe", "@xenoviz/ruk-windows-arm64", "ruk.exe"}},
}

// Resolve returns the release and npm names for a supported target.
func Resolve(target Target) (Mapping, error) {
	for _, supported := range supportedTargets {
		if supported.target == target {
			return supported.mapping, nil
		}
	}
	return Mapping{}, fmt.Errorf("%w: %s/%s%s", ErrUnsupportedTarget, target.GOOS, target.GOARCH, libcSuffix(target))
}

// SupportedTargets returns a copy of the currently published native target
// matrix. The returned slice can be changed by the caller safely.
func SupportedTargets() []Target {
	targets := make([]Target, 0, len(supportedTargets))
	for _, supported := range supportedTargets {
		targets = append(targets, supported.target)
	}
	return targets
}

// AssetName maps a Go platform target to its canonical GitHub Release asset.
func AssetName(target Target) (string, error) {
	mapping, err := Resolve(target)
	if err != nil {
		return "", err
	}
	return mapping.AssetName, nil
}

// PackageName maps a Go platform target to its platform npm package.
func PackageName(target Target) (string, error) {
	mapping, err := Resolve(target)
	if err != nil {
		return "", err
	}
	return mapping.PackageName, nil
}

// ExecutableName maps a Go platform target to the filename installed for the
// native executable.
func ExecutableName(target Target) (string, error) {
	mapping, err := Resolve(target)
	if err != nil {
		return "", err
	}
	return mapping.ExecutableName, nil
}

func libcSuffix(target Target) string {
	if target.Musl {
		return "/musl"
	}
	return ""
}
