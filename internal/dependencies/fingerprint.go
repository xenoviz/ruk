// Package dependencies contains the dependency-input and projection-integrity
// primitives used by workspace preparation. Installing or removing a
// projection deliberately lives outside this package.
package dependencies

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
)

const fingerprintVersion = "ruk-fingerprint-v3"

// PackageManager describes the inputs supplied by the selected installer.
// Command is copied before it is returned in FingerprintDetails, so callers
// may safely reuse or modify their input after fingerprinting.
type PackageManager struct {
	Name           string
	Version        string
	Command        []string
	DependencyMode string
}

// RuntimeIdentity identifies the runtime and native ABI that can affect an
// installed dependency tree. Platform and Architecture default to the host
// values when omitted; the remaining fields are intentionally caller-owned so
// a compatibility layer can provide Node, Bun, or Go runtime values.
type RuntimeIdentity struct {
	Platform     string
	Architecture string
	Runtime      string
	Version      string
	NativeABI    string
}

// SourceFingerprintInput is the complete, deterministic input to
// DependencyFingerprint. Files are repository-relative dependency inputs,
// normally the output of Git's tracked/non-ignored file listing filtered by
// DependencyFiles.
type SourceFingerprintInput struct {
	Root    string
	Files   []string
	Manager PackageManager
	Runtime RuntimeIdentity
}

// FingerprintDetails is the source fingerprint together with the normalized
// inputs used to produce it. It follows the state-facing dependency contract
// without importing the state package; runtime and filesystem metadata are
// represented using Go's platform-native values.
type FingerprintDetails struct {
	Fingerprint string
	Files       []string
	Manager     PackageManager
}

// DependencyFiles filters a repository file listing to dependency inputs.
// Nested package roots are retained, and paths are returned in deterministic
// slash-separated order. The caller remains responsible for obtaining the
// listing (for example through Git); this function does no process or network
// work.
func DependencyFiles(paths []string) []string {
	seen := make(map[string]struct{})
	selected := make([]string, 0, len(paths))
	for _, path := range paths {
		normalized := strings.ReplaceAll(path, `\`, "/")
		if normalized == "" {
			continue
		}
		name := normalized[strings.LastIndexByte(normalized, '/')+1:]
		if !dependencyName(name) && !inPatchDirectory(normalized) {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		selected = append(selected, normalized)
	}
	sort.Strings(selected)
	return selected
}

func dependencyName(name string) bool {
	switch name {
	case "package.json", "bun.lock", "bun.lockb", "bunfig.toml", "package-lock.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "yarn.lock", ".npmrc", ".yarnrc.yml", ".rukrc.json":
		return true
	default:
		return false
	}
}

func inPatchDirectory(path string) bool {
	for _, component := range strings.Split(strings.Trim(path, "/"), "/") {
		if component == "patches" {
			return true
		}
	}
	return false
}

// DependencyFingerprint hashes dependency source files and package-manager
// identity. Text files normalize CRLF to LF, while bun.lockb remains binary;
// a missing file is represented explicitly, allowing a later source listing to
// invalidate a prepared workspace. This preserves the dependency semantics of
// the TypeScript implementation without promising byte-for-byte hash parity
// where Go and Node expose different runtime or filesystem metadata.
func DependencyFingerprint(input SourceFingerprintInput) (FingerprintDetails, error) {
	files := DependencyFiles(input.Files)
	for index, path := range files {
		normalized, err := normalizeSourcePath(path)
		if err != nil {
			return FingerprintDetails{}, err
		}
		files[index] = normalized
	}
	sort.Strings(files)
	for index := 1; index < len(files); index++ {
		if files[index] == files[index-1] {
			files = append(files[:index], files[index+1:]...)
			index--
		}
	}

	manager := input.Manager
	if manager.DependencyMode == "" {
		manager.DependencyMode = "managed"
	}
	runtimeIdentity := input.Runtime
	if runtimeIdentity.Platform == "" {
		runtimeIdentity.Platform = runtime.GOOS
	}
	if runtimeIdentity.Architecture == "" {
		runtimeIdentity.Architecture = runtime.GOARCH
	}

	hash := sha256.New()
	writeFields(hash, fingerprintVersion)
	writeFields(hash, runtimeIdentity.Platform, runtimeIdentity.Architecture)
	writeFields(hash, runtimeIdentity.Runtime, runtimeIdentity.Version, runtimeIdentity.NativeABI)
	command, err := json.Marshal(manager.Command)
	if err != nil {
		return FingerprintDetails{}, fmt.Errorf("marshal dependency command: %w", err)
	}
	writeFields(hash, manager.Name, manager.Version, manager.DependencyMode, string(command))

	for _, relative := range files {
		writeFields(hash, relative)
		content, readErr := os.ReadFile(filepath.Join(input.Root, filepath.FromSlash(relative)))
		if errors.Is(readErr, fs.ErrNotExist) {
			writeFields(hash, "missing")
			continue
		}
		if readErr != nil {
			return FingerprintDetails{}, fmt.Errorf("read dependency input %q: %w", relative, readErr)
		}
		if filepath.Base(relative) != "bun.lockb" {
			content = []byte(strings.ReplaceAll(string(content), "\r\n", "\n"))
		}
		writeFields(hash, fmt.Sprintf("%d", len(content)))
		hash.Write(content)
		hash.Write([]byte{0})
	}

	return FingerprintDetails{
		Fingerprint: hex.EncodeToString(hash.Sum(nil)),
		Files:       files,
		Manager:     PackageManager{Name: manager.Name, Version: manager.Version, Command: append([]string(nil), manager.Command...), DependencyMode: manager.DependencyMode},
	}, nil
}

func writeFields(hash interface{ Write([]byte) (int, error) }, fields ...string) {
	for _, field := range fields {
		_, _ = hash.Write([]byte(field))
		_, _ = hash.Write([]byte{0})
	}
}

func normalizeSourcePath(value string) (string, error) {
	normalized := strings.ReplaceAll(value, `\`, "/")
	windowsVolume := len(normalized) >= 2 && normalized[1] == ':'
	if normalized == "" || strings.HasPrefix(normalized, "/") || windowsVolume || filepath.VolumeName(normalized) != "" {
		return "", fmt.Errorf("dependency input must be a non-empty relative path: %q", value)
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(normalized)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("dependency input must stay inside the repository: %q", value)
	}
	return cleaned, nil
}

// ProjectionFingerprint returns a deterministic SHA-256 fingerprint of one
// or more workspace-local dependency projections. It records file metadata,
// directory modes, symlink text, and the metadata/content shape of symlink
// targets. A projection path must be lexically inside root and cannot pass
// through a symlinked ancestor.
func ProjectionFingerprint(root string, projections []string) (string, error) {
	if len(projections) == 0 {
		return "", errors.New("at least one dependency projection is required")
	}
	resolvedRoot, err := absoluteClean(root)
	if err != nil {
		return "", fmt.Errorf("resolve dependency root: %w", err)
	}
	paths := append([]string(nil), projections...)
	sort.Strings(paths)
	hash := sha256.New()
	visited := make(map[string]struct{})
	for _, relative := range paths {
		target, label, err := projectionPath(resolvedRoot, relative)
		if err != nil {
			return "", err
		}
		if err := hashProjectionEntry(target, label, hash, visited); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ProjectionIntegrityValid reports whether every recorded projection exists
// and matches expected. Any malformed path, missing target, unreadable entry,
// symlink cycle, or other filesystem error returns false (fail closed).
func ProjectionIntegrityValid(root string, projections []string, expected string) bool {
	if expected == "" || len(projections) == 0 {
		return false
	}
	actual, err := ProjectionFingerprint(root, projections)
	return err == nil && actual == expected
}

// DependencyProjectionsAreValid is a descriptive alias for callers migrating
// the TypeScript dependencyProjectionsAreValid predicate.
func DependencyProjectionsAreValid(root string, projections []string, expected string) bool {
	return ProjectionIntegrityValid(root, projections, expected)
}

func projectionPath(root, relative string) (string, string, error) {
	if relative == "" {
		return "", "", errors.New("dependency projection path cannot be empty")
	}
	normalized := strings.ReplaceAll(relative, `\`, "/")
	if filepath.IsAbs(relative) || strings.HasPrefix(normalized, "/") || (len(normalized) >= 2 && normalized[1] == ':') {
		return "", "", fmt.Errorf("dependency projection must stay inside the workspace: %s", relative)
	}
	target := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+filepathSeparator) {
		return "", "", fmt.Errorf("dependency projection must stay inside the workspace: %s", relative)
	}
	for ancestor := filepath.Dir(target); ancestor != root; ancestor = filepath.Dir(ancestor) {
		info, statErr := os.Lstat(ancestor)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", "", fmt.Errorf("dependency projection has a symlinked ancestor: %s", relative)
			}
			continue
		}
		if errors.Is(statErr, fs.ErrNotExist) {
			// A missing child does not make an ancestor safe: continue toward
			// root so a symlinked parent cannot redirect a future projection.
			continue
		}
		return "", "", fmt.Errorf("inspect dependency projection ancestor %q: %w", ancestor, statErr)
	}
	return target, filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))), nil
}

const filepathSeparator = string(filepath.Separator)

func hashProjectionEntry(entry, label string, hash interface{ Write([]byte) (int, error) }, visited map[string]struct{}) error {
	info, err := os.Lstat(entry)
	if err != nil {
		return fmt.Errorf("inspect dependency projection %q: %w", label, err)
	}
	mode := info.Mode()
	if mode&os.ModeSymlink != 0 {
		target, err := os.Readlink(entry)
		if err != nil {
			return fmt.Errorf("read dependency projection link %q: %w", label, err)
		}
		writeFields(hash, "symlink", label, target)
		real, err := filepath.EvalSymlinks(entry)
		if err != nil {
			return fmt.Errorf("resolve dependency projection link %q: %w", label, err)
		}
		return hashProjectionEntry(real, label+"/@target", hash, visited)
	}
	if mode.IsDir() {
		writeFields(hash, "directory", label, fmt.Sprintf("%o", mode.Perm()))
		real, err := filepath.EvalSymlinks(entry)
		if err != nil {
			return fmt.Errorf("resolve dependency projection directory %q: %w", label, err)
		}
		if _, exists := visited[real]; exists {
			writeFields(hash, "visited-directory", label, real)
			return nil
		}
		visited[real] = struct{}{}
		entries, err := os.ReadDir(entry)
		if err != nil {
			return fmt.Errorf("read dependency projection directory %q: %w", label, err)
		}
		names := make([]string, 0, len(entries))
		for _, child := range entries {
			names = append(names, child.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			if err := hashProjectionEntry(filepath.Join(entry, name), label+"/"+name, hash, visited); err != nil {
				return err
			}
		}
		return nil
	}
	kind := "other"
	if mode.IsRegular() {
		kind = "file"
	}
	writeFields(hash, kind, label, fmt.Sprintf("%o", mode.Perm()), fmt.Sprintf("%d", info.Size()), fmt.Sprintf("%d", info.ModTime().UnixNano()), changeTime(info))
	return nil
}

func changeTime(info os.FileInfo) string {
	system := info.Sys()
	if system == nil {
		return ""
	}
	value := reflect.Indirect(reflect.ValueOf(system))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	for _, name := range []string{"Ctim", "Ctimespec", "ChangeTime"} {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			return fmt.Sprintf("%v", field.Interface())
		}
	}
	return ""
}

func absoluteClean(value string) (string, error) {
	if value == "" {
		value = "."
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
