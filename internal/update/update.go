// Package update owns trusted release discovery and self-update behavior.
//
// The package deliberately has no dependencies on the CLI.  Callers identify
// the distribution explicitly: package updates delegate to the package
// manager that owns the installation, while standalone updates replace the
// current executable.
package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
)

type Updater struct {
	discover        Discovery
	download        Download
	fileSystem      FileSystem
	run             CommandRunner
	runWithIO       InteractiveCommandRunner
	scheduleWindows WindowsScheduler
	httpClient      *http.Client
}

func New(hooks Hooks) *Updater {
	return &Updater{
		discover: hooks.Discover, download: hooks.Download, fileSystem: hooks.FileSystem,
		run: hooks.Run, runWithIO: hooks.RunWithIO, scheduleWindows: hooks.ScheduleWindows, httpClient: hooks.HTTPClient,
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
	// releases on that channel without requiring a second, easy-to-forget flag.
	// Stable installations still opt in explicitly. An explicit prerelease
	// request intentionally considers every prerelease channel.
	allowPrerelease := options.AllowPrerelease || len(parsedCurrent.Prerelease) != 0
	prereleaseChannel := ""
	if !options.AllowPrerelease && len(parsedCurrent.Prerelease) != 0 {
		prereleaseChannel = parsedCurrent.Prerelease[0]
	}
	release, err := latestReady(candidates, allowPrerelease, prereleaseChannel)
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
			platform = RuntimePlatform()
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
		// The package postinstall may need to replace this executable on
		// Windows. Pass the current PID only to the package-manager child so
		// the npm launcher can defer that replacement until this process exits.
		ctx = context.WithValue(ctx, packageUpdatePIDKey{}, os.Getpid())
		result, err := updater.runPackageCommand(ctx, command, args, CommandIO{
			Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr,
			MachineReadable: options.MachineReadable,
		})
		if err != nil {
			return Result{}, err
		}
		if result.ExitCode != 0 {
			return Result{}, fmt.Errorf("package manager %s failed with exit code %d", installer, result.ExitCode)
		}
		status := StatusUpdated
		if updatePlatformOS(options.Platform) == "windows" {
			status = StatusScheduled
		}
		return Result{Status: status, CurrentVersion: current, LatestVersion: latest.String(), Method: string(installer), Asset: nil}, nil
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

func (updater *Updater) runner() CommandRunner {
	if updater.run != nil {
		return updater.run
	}
	return OSCommandRunner
}

func (updater *Updater) runPackageCommand(ctx context.Context, command string, args []string, commandIO CommandIO) (CommandResult, error) {
	if commandIO.MachineReadable {
		// Never pass terminal streams into package managers in JSON mode: an
		// installer prompt could hang automation, and any output would corrupt
		// the caller's single machine-readable record.
		commandIO.Stdin, commandIO.Stdout, commandIO.Stderr = nil, nil, nil
	}
	if updater.runWithIO != nil {
		return updater.runWithIO(ctx, command, args, commandIO)
	}
	if updater.run != nil {
		return updater.run(ctx, command, args)
	}
	return OSCommandRunnerWithIO(ctx, command, args, commandIO)
}
