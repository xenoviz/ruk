package update

import (
	"context"
	"io"
	"io/fs"
	"net/http"
)

const (
	Repository     = "xenoviz/ruk"
	PackageName    = "@xenoviz/ruk"
	MaxBinaryBytes = int64(250 * 1024 * 1024)
	MaxCommandTail = 64 * 1024
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

type packageUpdatePIDKey struct{}

// CommandIO controls the terminal streams used by an update command. Human
// updates may inherit these streams while retaining bounded diagnostic tails;
// machine-readable updates keep the streams private so they cannot corrupt
// the single JSON record emitted by the CLI.
type CommandIO struct {
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	MachineReadable bool
}

// InteractiveCommandRunner is an optional update command seam that receives
// the requested terminal policy. Run remains available for compatibility with
// callers that only need captured output.
type InteractiveCommandRunner func(context.Context, string, []string, CommandIO) (CommandResult, error)

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
	RunWithIO       InteractiveCommandRunner
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
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	MachineReadable bool
}
