package update

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

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
		// The CLI resolves symlinks before composing update options. Use that
		// resolved entrypoint for standalone replacement so an invocation via a
		// symlink updates the installed target rather than destroying the link.
		executable = options.Entrypoint
		if executable == "" {
			executable, err = os.Executable()
			if err != nil {
				return Result{}, err
			}
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
	// FileSystem.Copy intentionally creates security-sensitive backups with a
	// restrictive mode. Restore the original executable permissions before the
	// backup can become the rollback target.
	if err := fsys.Chmod(backup, metadata.Mode()&0o777); err != nil {
		if removeErr := fsys.Remove(backup); removeErr != nil {
			return fmt.Errorf("%w (backup cleanup failed: %v)", err, removeErr)
		}
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

func randomSuffix() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate update artifact name: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// WindowsReplacementPlan returns a conservative detached helper script. It
// waits for the running executable's file lock with a bounded retry count; on
// exhaustion it leaves the candidate, backup, and helper for recovery. It
// never polls or signals a numeric PID, so PID reuse cannot authorize an
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
		"set /A waitAttempts=0\r\n" +
		"set /A rollbackAttempts=0\r\n" +
		":wait\r\n" +
		"set /A waitAttempts+=1\r\n" +
		"if %waitAttempts% GEQ 120 goto wait_failed\r\n" +
		"copy /Y " + quote(executable) + " " + quote(backup) + " >NUL 2>NUL\r\n" +
		"if errorlevel 1 (timeout /t 1 /nobreak >NUL & goto wait)\r\n" +
		"move /Y " + quote(candidate) + " " + quote(executable) + " >NUL 2>NUL\r\n" +
		"if errorlevel 1 (timeout /t 1 /nobreak >NUL & goto wait)\r\n" +
		quote(executable) + " --version | findstr /X \"" + version + "\" >NUL || goto rollback\r\n" +
		"del /Q " + quote(backup) + " >NUL 2>NUL\r\n" +
		"del /Q " + quote(helper) + " >NUL 2>NUL\r\nexit /B 0\r\n" +
		":wait_failed\r\nexit /B 1\r\n" +
		":rollback\r\nset /A rollbackAttempts+=1\r\n" +
		"move /Y " + quote(backup) + " " + quote(executable) + " >NUL 2>NUL\r\n" +
		"if not errorlevel 1 goto rollback_succeeded\r\n" +
		"if %rollbackAttempts% GEQ 120 goto rollback_failed\r\n" +
		"timeout /t 1 /nobreak >NUL\r\ngoto rollback\r\n" +
		":rollback_succeeded\r\n:rollback_failed\r\ndel /Q " + quote(candidate) + " >NUL 2>NUL\r\ndel /Q " + quote(helper) + " >NUL 2>NUL\r\nexit /B 1\r\n"
	return helper, script, nil
}
