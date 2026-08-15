// Package update owns trusted release discovery and self-update behavior.
//
// The package deliberately has no dependencies on the CLI.  Callers identify
// the distribution explicitly: package updates delegate to the package
// manager that owns the installation, while standalone updates replace the
// current executable.
package update

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	Repository       = "xenoviz/ruk"
	PackageName      = "@xenoviz/ruk"
	ReleasesURL      = "https://api.github.com/repos/xenoviz/ruk/releases?per_page=10"
	MaxBinaryBytes   = int64(250 * 1024 * 1024)
	MaxCommandTail   = 64 * 1024
	defaultUserAgent = "ruk-go"
)

// Distribution is intentionally not inferred from the executable path.  The
// package and standalone launchers have different update ownership semantics.
type Distribution string

const (
	DistributionPackage    Distribution = "package"
	DistributionStandalone Distribution = "standalone"
	// Short names make composition code concise while retaining explicit type
	// checking at the call site.
	Package    = DistributionPackage
	Standalone = DistributionStandalone
)

type Installer string

const (
	InstallerBun  Installer = "bun"
	InstallerNPM  Installer = "npm"
	InstallerPNPM Installer = "pnpm"
	InstallerYarn Installer = "yarn"
)

type Platform struct {
	OS           string
	Architecture string
	Musl         bool
}

type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"url,omitempty"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	SchemaVersion int              `json:"schemaVersion"`
	Repository    string           `json:"repository"`
	Version       string           `json:"version"`
	Package       ManifestPackage  `json:"package"`
	Assets        map[string]Asset `json:"assets"`
}

type ManifestPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Release is a ready candidate returned by Discovery.  A custom discovery
// hook may populate the manifest directly; HTTP discovery validates the
// readiness manifest before returning a release.
type Release struct {
	Version    string           `json:"version"`
	Tag        string           `json:"tag_name,omitempty"`
	Draft      bool             `json:"draft,omitempty"`
	Prerelease bool             `json:"prerelease,omitempty"`
	Assets     map[string]Asset `json:"assets"`
	Manifest   *Manifest        `json:"manifest,omitempty"`
}

type ResultStatus string

const (
	StatusUpToDate        ResultStatus = "up-to-date"
	StatusUpdateAvailable ResultStatus = "update-available"
	StatusUpdated         ResultStatus = "updated"
	StatusScheduled       ResultStatus = "scheduled"
)

type Result struct {
	Status         ResultStatus `json:"status"`
	CurrentVersion string       `json:"currentVersion"`
	LatestVersion  string       `json:"latestVersion"`
	Method         string       `json:"method"`
	Asset          *string      `json:"asset"`
}

// Discovery and Download are injected seams for compatibility tests and for
// callers that provide a release mirror.  They must not mutate their inputs.
type Discovery func(context.Context) ([]Release, error)
type Download func(context.Context, Asset) ([]byte, error)

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type CommandRunner func(context.Context, string, []string) (CommandResult, error)

// FileSystem contains the operations needed by atomic replacement.  WriteFile
// must fail when path already exists (the default implementation uses O_EXCL).
type FileSystem interface {
	Stat(string) (fs.FileInfo, error)
	Chmod(string, fs.FileMode) error
	Copy(string, string) error
	Rename(string, string) error
	WriteFile(string, []byte, fs.FileMode) error
	Remove(string) error
}

// WindowsScheduler takes ownership of candidate after returning nil.  The
// candidate must be replaced only after the current process exits.
type WindowsScheduler func(context.Context, string, string, string) error

type Hooks struct {
	Discover        Discovery
	Download        Download
	FileSystem      FileSystem
	Run             CommandRunner
	ScheduleWindows WindowsScheduler
	HTTPClient      *http.Client
}

type Options struct {
	Distribution    Distribution
	CurrentVersion  string
	CheckOnly       bool
	Installer       Installer
	Entrypoint      string
	Platform        Platform
	Executable      string
	AllowPrerelease bool
}

type Updater struct {
	discover        Discovery
	download        Download
	fileSystem      FileSystem
	run             CommandRunner
	scheduleWindows WindowsScheduler
	httpClient      *http.Client
}

func New(hooks Hooks) *Updater {
	return &Updater{
		discover: hooks.Discover, download: hooks.Download, fileSystem: hooks.FileSystem,
		run: hooks.Run, scheduleWindows: hooks.ScheduleWindows, httpClient: hooks.HTTPClient,
	}
}

// Update runs one explicit update operation.  No network request is made by
// ordinary commands unless this function is called.
func (updater *Updater) Update(ctx context.Context, options Options) (Result, error) {
	if options.Distribution != DistributionPackage && options.Distribution != DistributionStandalone {
		return Result{}, errors.New("update distribution must be package or standalone")
	}
	current := options.CurrentVersion
	if current == "" {
		current = "0.0.0"
	}
	parsedCurrent, err := ParseVersion(current)
	if err != nil {
		return Result{}, err
	}
	current = parsedCurrent.String()
	discover := updater.discovery()
	candidates, err := discover(ctx)
	if err != nil {
		return Result{}, err
	}
	// Once a caller is already on a prerelease channel, keep discovering newer
	// prereleases without requiring a second, easy-to-forget flag. Stable
	// installations still opt in explicitly.
	allowPrerelease := options.AllowPrerelease || len(parsedCurrent.Prerelease) != 0
	release, err := latestReady(candidates, allowPrerelease)
	if err != nil {
		return Result{}, err
	}
	latest, _ := ParseVersion(release.Version)
	method := string(options.Distribution)
	installer := options.Installer
	if options.Distribution == DistributionPackage {
		if installer == "" {
			if configured := os.Getenv("RUK_UPDATE_INSTALLER"); configured != "" {
				installer, err = ParseInstaller(configured)
				if err != nil {
					return Result{}, err
				}
			} else {
				installer, err = DetectInstaller(options.Entrypoint)
				if err != nil {
					return Result{}, err
				}
			}
		}
		method = string(installer)
	}
	assetName := ""
	if options.Distribution == DistributionStandalone {
		platform := options.Platform
		if platform.OS == "" {
			platform.OS = runtimeGOOS()
		}
		if platform.Architecture == "" {
			platform.Architecture = runtimeGOARCH()
		}
		assetName, err = ExecutableAsset(platform)
		if err != nil {
			return Result{}, err
		}
	}
	var resultAsset *string
	if assetName != "" {
		resultAsset = &assetName
	}
	if latest.Compare(parsedCurrent) <= 0 {
		return Result{Status: StatusUpToDate, CurrentVersion: current, LatestVersion: latest.String(), Method: method, Asset: resultAsset}, nil
	}
	if options.CheckOnly {
		return Result{Status: StatusUpdateAvailable, CurrentVersion: current, LatestVersion: latest.String(), Method: method, Asset: resultAsset}, nil
	}
	if options.Distribution == DistributionPackage {
		command, args, err := InstallerCommand(installer, latest.String())
		if err != nil {
			return Result{}, err
		}
		runner := updater.runner()
		result, err := runner(ctx, command, args)
		if err != nil {
			return Result{}, err
		}
		if result.ExitCode != 0 {
			return Result{}, fmt.Errorf("package manager %s failed with exit code %d", installer, result.ExitCode)
		}
		return Result{Status: StatusUpdated, CurrentVersion: current, LatestVersion: latest.String(), Method: string(installer), Asset: nil}, nil
	}
	return updater.updateStandalone(ctx, release, latest.String(), assetName, options, current)
}

// Run is a convenient one-shot API for callers that do not need to retain an
// Updater.  Hooks in options are intentionally separate so Distribution stays
// an explicit input rather than an environment-derived mode.
func Run(ctx context.Context, options Options, hooks Hooks) (Result, error) {
	return New(hooks).Update(ctx, options)
}

// Update is an alias for Run for integrations that prefer a package-level
// operation.
func Update(ctx context.Context, options Options, hooks Hooks) (Result, error) {
	return Run(ctx, options, hooks)
}

// SelectLatest exposes the same release selection used by Update to release
// tooling and compatibility harnesses.
func SelectLatest(candidates []Release, allowPrerelease bool) (Release, error) {
	return latestReady(candidates, allowPrerelease)
}

func latestReady(candidates []Release, allowPrerelease bool) (Release, error) {
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

func (updater *Updater) discovery() Discovery {
	if updater.discover != nil {
		return updater.discover
	}
	return updater.discoverHTTP
}

func (updater *Updater) discoverHTTP(ctx context.Context) ([]Release, error) {
	client := updater.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ReleasesURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", defaultUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("update request failed (%s)", response.Status)
	}
	var remote []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&remote); err != nil {
		return nil, errors.New("GitHub returned an invalid release list")
	}
	result := make([]Release, 0, len(remote))
	for _, item := range remote {
		version, err := ParseVersion(item.Tag)
		if err != nil || item.Draft {
			continue
		}
		assets := make(map[string]Asset, len(item.Assets))
		for _, asset := range item.Assets {
			assets[asset.Name] = Asset{Name: asset.Name, URL: asset.URL}
		}
		manifestAsset, ok := assets["ruk-release.json"]
		if !ok {
			continue
		}
		if err := validateAssetURL(manifestAsset, version.String()); err != nil {
			continue
		}
		manifestBytes, err := updater.downloadFunc()(ctx, manifestAsset)
		if err != nil {
			continue
		}
		var manifest Manifest
		if json.Unmarshal(manifestBytes, &manifest) != nil || ValidateManifest(manifest, version.String()) != nil {
			continue
		}
		validAssets := true
		for name, metadata := range manifest.Assets {
			asset, ok := assets[name]
			if !ok {
				validAssets = false
				break
			}
			asset.SHA256, asset.Size = metadata.SHA256, metadata.Size
			assets[name] = asset
		}
		if !validAssets {
			continue
		}
		result = append(result, Release{Version: version.String(), Tag: item.Tag, Prerelease: item.Prerelease, Assets: assets, Manifest: &manifest})
	}
	return result, nil
}

func (updater *Updater) downloadFunc() Download {
	if updater.download != nil {
		return updater.download
	}
	return func(ctx context.Context, asset Asset) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/octet-stream")
		request.Header.Set("User-Agent", defaultUserAgent)
		client := updater.httpClient
		if client == nil {
			client = http.DefaultClient
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("update request failed (%s)", response.Status)
		}
		if response.ContentLength > MaxBinaryBytes {
			return nil, fmt.Errorf("%s exceeds the update size limit", asset.Name)
		}
		bytes, err := io.ReadAll(io.LimitReader(response.Body, MaxBinaryBytes+1))
		if err != nil {
			return nil, err
		}
		if len(bytes) == 0 || int64(len(bytes)) > MaxBinaryBytes {
			return nil, fmt.Errorf("%s has an invalid download size", asset.Name)
		}
		return bytes, nil
	}
}

func (updater *Updater) updateStandalone(ctx context.Context, release Release, version, assetName string, options Options, current string) (Result, error) {
	asset, ok := release.Assets[assetName]
	if !ok {
		return Result{}, fmt.Errorf("release %s does not contain %s", version, assetName)
	}
	if asset.Name == "" {
		asset.Name = assetName
	}
	if asset.URL != "" {
		if err := validateAssetURL(asset, version); err != nil {
			return Result{}, err
		}
	}
	bytes, err := updater.downloadFunc()(ctx, asset)
	if err != nil {
		return Result{}, err
	}
	if int64(len(bytes)) <= 0 || int64(len(bytes)) > MaxBinaryBytes {
		return Result{}, fmt.Errorf("%s has an invalid download size", assetName)
	}
	if asset.Size > 0 && int64(len(bytes)) != asset.Size {
		return Result{}, fmt.Errorf("release manifest size does not match %s", assetName)
	}
	if asset.SHA256 != "" {
		actual := sha256.Sum256(bytes)
		expected, decodeErr := hex.DecodeString(asset.SHA256)
		if decodeErr != nil || len(expected) != sha256.Size || subtle.ConstantTimeCompare(actual[:], expected) != 1 {
			return Result{}, fmt.Errorf("Checksum verification failed for %s", assetName)
		}
	}
	fsys := updater.fileSystem
	if fsys == nil {
		fsys = OSFileSystem{}
	}
	executable := options.Executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return Result{}, err
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return Result{}, err
	}
	candidate := filepath.Join(filepath.Dir(executable), "."+filepath.Base(executable)+".ruk-"+version+"-"+strconv.Itoa(os.Getpid())+".new")
	if err := fsys.WriteFile(candidate, bytes, 0o755); err != nil {
		return Result{}, err
	}
	ownedCandidate := true
	defer func() {
		if ownedCandidate {
			_ = fsys.Remove(candidate)
		}
	}()
	if runtimeGOOS() == "windows" || options.Platform.OS == "windows" {
		schedule := updater.scheduleWindows
		if schedule == nil {
			schedule = updater.defaultWindowsScheduler(fsys)
		}
		if err := schedule(ctx, executable, candidate, version); err != nil {
			return Result{}, err
		}
		ownedCandidate = false
		assetPointer := assetName
		return Result{Status: StatusScheduled, CurrentVersion: current, LatestVersion: version, Method: string(DistributionStandalone), Asset: &assetPointer}, nil
	}
	if err := replacePOSIX(fsys, updater.runner(), ctx, executable, candidate, version); err != nil {
		return Result{}, err
	}
	ownedCandidate = false
	assetPointer := assetName
	return Result{Status: StatusUpdated, CurrentVersion: current, LatestVersion: version, Method: string(DistributionStandalone), Asset: &assetPointer}, nil
}

func replacePOSIX(fsys FileSystem, runner CommandRunner, ctx context.Context, executable, candidate, version string) error {
	metadata, err := fsys.Stat(executable)
	if err != nil {
		return err
	}
	if err := fsys.Chmod(candidate, metadata.Mode()&0o777); err != nil {
		return err
	}
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	backup := executable + ".ruk-backup-" + suffix
	if err := fsys.Copy(executable, backup); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if restoreErr := fsys.Rename(backup, executable); restoreErr != nil {
			return fmt.Errorf("%w (rollback failed: %v)", cause, restoreErr)
		}
		return cause
	}
	if err := fsys.Rename(candidate, executable); err != nil {
		return rollback(err)
	}
	verified, err := runner(ctx, executable, []string{"--version"})
	if err != nil {
		return rollback(err)
	}
	if verified.ExitCode != 0 || strings.TrimSpace(verified.Stdout) != version {
		return rollback(errors.New("The replacement executable failed its version check"))
	}
	if err := fsys.Remove(backup); err != nil {
		return err
	}
	return nil
}

func (updater *Updater) runner() CommandRunner {
	if updater.run != nil {
		return updater.run
	}
	return OSCommandRunner
}

func (updater *Updater) defaultWindowsScheduler(fsys FileSystem) WindowsScheduler {
	return func(ctx context.Context, executable, candidate, version string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		helper, script, err := WindowsReplacementPlan(executable, candidate, version, os.Getpid())
		if err != nil {
			return err
		}
		if err := fsys.WriteFile(helper, []byte(script), 0o700); err != nil {
			return err
		}
		command := exec.Command("cmd.exe", "/d", "/s", "/c", helper)
		if err := command.Start(); err != nil {
			_ = fsys.Remove(helper)
			return err
		}
		return nil
	}
}

// OSFileSystem is the production filesystem implementation.
type OSFileSystem struct{}

func (OSFileSystem) Stat(name string) (fs.FileInfo, error)     { return os.Stat(name) }
func (OSFileSystem) Chmod(name string, mode fs.FileMode) error { return os.Chmod(name, mode) }
func (OSFileSystem) Copy(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	// Backups are security-sensitive update artifacts. Never follow or truncate
	// a pre-created path in a directory writable by another process.
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func (OSFileSystem) Rename(oldName, newName string) error { return os.Rename(oldName, newName) }
func (OSFileSystem) WriteFile(name string, bytes []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	_, writeErr := file.Write(bytes)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
func (OSFileSystem) Remove(name string) error {
	err := os.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func OSCommandRunner(ctx context.Context, command string, args []string) (CommandResult, error) {
	process := exec.CommandContext(ctx, command, args...)
	stdout, stderr := newTailBuffer(MaxCommandTail), newTailBuffer(MaxCommandTail)
	process.Stdout, process.Stderr = stdout, stderr
	err := process.Run()
	if err == nil {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitErr.ExitCode()}, nil
	}
	return CommandResult{}, err
}

// tailBuffer retains only the most recent bytes from a command. Package
// managers and custom wrappers can be arbitrarily noisy; updater diagnostics
// must stay useful without letting suppressed output grow memory without bound.
type tailBuffer struct {
	bytes []byte
	limit int
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (buffer *tailBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if buffer.limit <= 0 {
		return written, nil
	}
	if len(value) >= buffer.limit {
		buffer.bytes = append(buffer.bytes[:0], value[len(value)-buffer.limit:]...)
		return written, nil
	}
	overflow := len(buffer.bytes) + len(value) - buffer.limit
	if overflow > 0 {
		copy(buffer.bytes, buffer.bytes[overflow:])
		buffer.bytes = buffer.bytes[:len(buffer.bytes)-overflow]
	}
	buffer.bytes = append(buffer.bytes, value...)
	return written, nil
}

func (buffer *tailBuffer) String() string { return string(buffer.bytes) }

func randomSuffix() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate update artifact name: %w", err)
	}
	return hex.EncodeToString(value), nil
}

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
		return "bun", []string{"add", "--global", specification}, nil
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

// WindowsReplacementPlan returns a conservative detached helper script. It
// waits for the running executable's file lock by retrying the replacement;
// it never polls or signals a numeric PID, so PID reuse cannot authorize an
// unrelated process. The helper stages a backup, verifies the replacement, and
// restores the backup when verification fails.
func WindowsReplacementPlan(executable, candidate, version string, pid int) (string, string, error) {
	for _, value := range []string{executable, candidate, version} {
		if strings.ContainsAny(value, "\"\r\n&|<>^%!") {
			return "", "", errors.New("Executable path cannot be safely updated")
		}
	}
	helper := candidate + ".cmd"
	backup := candidate + ".backup"
	quote := func(value string) string { return `"` + value + `"` }
	script := "@echo off\r\n" +
		":wait\r\n" +
		"copy /Y " + quote(executable) + " " + quote(backup) + " >NUL 2>NUL\r\n" +
		"if errorlevel 1 (timeout /t 1 /nobreak >NUL & goto wait)\r\n" +
		"move /Y " + quote(candidate) + " " + quote(executable) + " >NUL 2>NUL\r\n" +
		"if errorlevel 1 (timeout /t 1 /nobreak >NUL & goto wait)\r\n" +
		quote(executable) + " --version | findstr /X \"" + version + "\" >NUL || goto rollback\r\n" +
		"del /Q " + quote(backup) + " >NUL 2>NUL\r\n" +
		"del /Q " + quote(helper) + " >NUL 2>NUL\r\nexit /B 0\r\n" +
		":rollback\r\nmove /Y " + quote(backup) + " " + quote(executable) + " >NUL\r\n:fail\r\ndel /Q " + quote(candidate) + " >NUL 2>NUL\r\ndel /Q " + quote(backup) + " >NUL 2>NUL\r\ndel /Q " + quote(helper) + " >NUL 2>NUL\r\nexit /B 1\r\n"
	return helper, script, nil
}

// runtime helpers are variables to keep platform naming testable without
// requiring platform-specific files in this foundational package.
var runtimeGOOS = func() string { return runtime.GOOS }
var runtimeGOARCH = func() string { return runtime.GOARCH }

// Version implements SemVer precedence, including numeric and textual
// prerelease identifiers. Build metadata is intentionally rejected because
// release tags and readiness manifests use the canonical public version.
type Version struct {
	Major, Minor, Patch uint64
	Prerelease          []string
}

func ParseVersion(value string) (Version, error) {
	value = strings.TrimPrefix(value, "v")
	if strings.Contains(value, "+") {
		return Version{}, fmt.Errorf("Unsupported version %s", value)
	}
	parts := strings.SplitN(value, "-", 2)
	if len(parts) == 0 || parts[0] == "" {
		return Version{}, fmt.Errorf("Unsupported version %s", value)
	}
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return Version{}, fmt.Errorf("Unsupported version %s", value)
	}
	parsed := Version{}
	values := []*uint64{&parsed.Major, &parsed.Minor, &parsed.Patch}
	for index, text := range core {
		if text == "" || (len(text) > 1 && text[0] == '0') {
			return Version{}, fmt.Errorf("Unsupported version %s", value)
		}
		number, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("Unsupported version %s", value)
		}
		*values[index] = number
	}
	if len(parts) == 2 {
		if parts[1] == "" {
			return Version{}, fmt.Errorf("Unsupported version %s", value)
		}
		for _, identifier := range strings.Split(parts[1], ".") {
			if identifier == "" || !isSemVerIdentifier(identifier) || (isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0') {
				return Version{}, fmt.Errorf("Unsupported version %s", value)
			}
			parsed.Prerelease = append(parsed.Prerelease, identifier)
		}
	}
	return parsed, nil
}

func isSemVerIdentifier(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') && character != '-' {
			return false
		}
	}
	return value != ""
}

func (version Version) String() string {
	value := fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Patch)
	if len(version.Prerelease) != 0 {
		value += "-" + strings.Join(version.Prerelease, ".")
	}
	return value
}

func (version Version) Compare(other Version) int {
	for _, pair := range [][2]uint64{{version.Major, other.Major}, {version.Minor, other.Minor}, {version.Patch, other.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(version.Prerelease) == 0 || len(other.Prerelease) == 0 {
		if len(version.Prerelease) == len(other.Prerelease) {
			return 0
		}
		if len(version.Prerelease) == 0 {
			return 1
		}
		return -1
	}
	for index := 0; index < len(version.Prerelease) && index < len(other.Prerelease); index++ {
		left, right := version.Prerelease[index], other.Prerelease[index]
		if left == right {
			continue
		}
		leftNumeric, rightNumeric := isNumeric(left), isNumeric(right)
		if leftNumeric && rightNumeric {
			left = strings.TrimLeft(left, "0")
			right = strings.TrimLeft(right, "0")
			if left == "" {
				left = "0"
			}
			if right == "" {
				right = "0"
			}
			if len(left) < len(right) {
				return -1
			}
			if len(left) > len(right) {
				return 1
			}
			if left < right {
				return -1
			}
			if left > right {
				return 1
			}
			continue
		}
		if leftNumeric != rightNumeric {
			if leftNumeric {
				return -1
			}
			return 1
		}
		if left < right {
			return -1
		}
		return 1
	}
	if len(version.Prerelease) < len(other.Prerelease) {
		return -1
	}
	if len(version.Prerelease) > len(other.Prerelease) {
		return 1
	}
	return 0
}

func CompareVersions(left, right string) (int, error) {
	leftVersion, err := ParseVersion(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := ParseVersion(right)
	if err != nil {
		return 0, err
	}
	return leftVersion.Compare(rightVersion), nil
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
