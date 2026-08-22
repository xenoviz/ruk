package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// SelectLatest exposes the same release selection used by Update to release
// tooling and compatibility harnesses.
func SelectLatest(candidates []Release, allowPrerelease bool) (Release, error) {
	return latestReady(candidates, allowPrerelease, "")
}

func latestReady(candidates []Release, allowPrerelease bool, prereleaseChannel string) (Release, error) {
	var selected Release
	var selectedVersion Version
	found := false
	for _, candidate := range candidates {
		if candidate.Draft {
			continue
		}
		versionText := candidate.Version
		if versionText == "" {
			versionText = candidate.Tag
		}
		version, err := ParseVersion(versionText)
		if err != nil {
			continue
		}
		candidate.Version = version.String()
		if !allowPrerelease && (candidate.Prerelease || len(version.Prerelease) != 0) {
			continue
		}
		if prereleaseChannel != "" && (len(version.Prerelease) == 0 || version.Prerelease[0] != prereleaseChannel) {
			continue
		}
		if candidate.Manifest != nil {
			if err := ValidateManifest(*candidate.Manifest, candidate.Version); err != nil {
				continue
			}
		}
		if !found || version.Compare(selectedVersion) > 0 {
			selected, selectedVersion, found = candidate, version, true
		}
	}
	if !found {
		return Release{}, errors.New("No completed Ruk release is available yet")
	}
	return selected, nil
}

func validateAssetURL(asset Asset, version string) error {
	parsed, err := url.Parse(asset.URL)
	if err != nil {
		return fmt.Errorf("Release %s contains an untrusted URL for %s", version, asset.Name)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if parsed.Scheme != "https" || parsed.Host != "github.com" || len(segments) != 6 ||
		segments[0] != "xenoviz" || segments[1] != "ruk" || segments[2] != "releases" || segments[3] != "download" ||
		(strings.TrimPrefix(segments[4], "v") != version) || segments[5] != asset.Name {
		return fmt.Errorf("Release %s contains an untrusted URL for %s", version, asset.Name)
	}
	return nil
}

func ValidateManifest(manifest Manifest, version string) error {
	parsed, err := ParseVersion(version)
	if err != nil {
		return err
	}
	if manifest.SchemaVersion != 1 || manifest.Repository != Repository || manifest.Version != parsed.String() || manifest.Package.Name != PackageName || manifest.Package.Version != parsed.String() {
		return fmt.Errorf("Release %s has an invalid readiness manifest", parsed.String())
	}
	if len(manifest.Assets) != 7 {
		return fmt.Errorf("Release %s readiness manifest has an invalid asset set", parsed.String())
	}
	for _, name := range []string{"ruk-linux-x64", "ruk-linux-arm64", "ruk-linux-x64-musl", "ruk-macos-x64", "ruk-macos-arm64", "ruk-windows-x64.exe", "ruk-windows-arm64.exe"} {
		asset, ok := manifest.Assets[name]
		if !ok || asset.Size <= 0 || asset.Size > MaxBinaryBytes || !isSHA256(asset.SHA256) {
			return fmt.Errorf("Release %s readiness manifest has invalid metadata for %s", parsed.String(), name)
		}
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
