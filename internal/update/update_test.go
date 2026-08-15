package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRelease(version string, bytes []byte) Release {
	digest := sha256.Sum256(bytes)
	return Release{Version: version, Assets: map[string]Asset{
		"ruk-linux-x64": {Name: "ruk-linux-x64", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(bytes))},
	}}
}

func TestUpdateStateTable(t *testing.T) {
	bytes := []byte("new executable")
	tests := []struct {
		name         string
		distribution Distribution
		current      string
		checkOnly    bool
		want         ResultStatus
		wantCommand  bool
	}{
		{name: "check available", distribution: DistributionStandalone, current: "0.2.0", checkOnly: true, want: StatusUpdateAvailable},
		{name: "noop", distribution: DistributionStandalone, current: "0.3.0", want: StatusUpToDate},
		{name: "package update", distribution: DistributionPackage, current: "0.2.0", want: StatusUpdated, wantCommand: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			updater := New(Hooks{
				Discover: func(context.Context) ([]Release, error) { return []Release{testRelease("0.3.0", bytes)}, nil },
				Run: func(_ context.Context, command string, args []string) (CommandResult, error) {
					called = true
					if command != "npm" || strings.Join(args, " ") != "install --global @xenoviz/ruk@0.3.0" {
						t.Fatalf("unexpected package command: %s %s", command, args)
					}
					return CommandResult{}, nil
				},
			})
			result, err := updater.Update(context.Background(), Options{
				Distribution: test.distribution, CurrentVersion: test.current, CheckOnly: test.checkOnly,
				Platform: Platform{OS: "linux", Architecture: "x64"}, Entrypoint: "/usr/local/bin/ruk",
			})
			if test.distribution == DistributionStandalone && test.want == StatusUpdated {
				// The table intentionally covers discovery/status separately; replacement
				// is exercised by TestStandaloneRollback below.
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.want {
				t.Fatalf("status = %s, want %s", result.Status, test.want)
			}
			if called != test.wantCommand {
				t.Fatalf("package runner called = %v, want %v", called, test.wantCommand)
			}
		})
	}
}

func TestPrereleaseSelection(t *testing.T) {
	updater := New(Hooks{Discover: func(context.Context) ([]Release, error) {
		return []Release{testRelease("0.3.0-beta.1", []byte("beta")), testRelease("0.2.9", []byte("old"))}, nil
	}})
	result, err := updater.Update(context.Background(), Options{
		Distribution: DistributionPackage, CurrentVersion: "0.2.0", CheckOnly: true, AllowPrerelease: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.LatestVersion != "0.3.0-beta.1" || result.Status != StatusUpdateAvailable {
		t.Fatalf("unexpected prerelease result: %+v", result)
	}
	compare, err := CompareVersions("0.3.0-beta.1", "0.3.0")
	if err != nil || compare >= 0 {
		t.Fatalf("prerelease comparison = %d, %v", compare, err)
	}
}

func TestCurrentPrereleaseStaysOnPrereleaseChannel(t *testing.T) {
	updater := New(Hooks{Discover: func(context.Context) ([]Release, error) {
		return []Release{testRelease("0.3.0-beta.2", []byte("beta")), testRelease("0.2.9", []byte("stable"))}, nil
	}})
	result, err := updater.Update(context.Background(), Options{
		Distribution: DistributionPackage, CurrentVersion: "0.3.0-beta.1", CheckOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.LatestVersion != "0.3.0-beta.2" || result.Status != StatusUpdateAvailable {
		t.Fatalf("unexpected prerelease result: %+v", result)
	}
}

func TestPrereleasePackageUpdateDelegatesExactVersion(t *testing.T) {
	var command string
	var args []string
	updater := New(Hooks{
		Discover: func(context.Context) ([]Release, error) {
			return []Release{testRelease("0.3.0-beta.1", []byte("beta"))}, nil
		},
		Run: func(_ context.Context, got string, gotArgs []string) (CommandResult, error) {
			command, args = got, append([]string(nil), gotArgs...)
			return CommandResult{}, nil
		},
	})
	result, err := updater.Update(context.Background(), Options{Distribution: DistributionPackage, CurrentVersion: "0.2.0", AllowPrerelease: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUpdated || command != "npm" || strings.Join(args, " ") != "install --global @xenoviz/ruk@0.3.0-beta.1" {
		t.Fatalf("prerelease update = %+v command=%s args=%v", result, command, args)
	}
}

func TestParseVersionRejectsInvalidPrereleaseCharacters(t *testing.T) {
	for _, version := range []string{"0.3.0-beta/1", "0.3.0-beta_1", "0.3.0-β", "0.3.0-beta 1"} {
		if _, err := ParseVersion(version); err == nil {
			t.Fatalf("ParseVersion(%q) succeeded", version)
		}
	}
}

func TestCommandTailIsBounded(t *testing.T) {
	buffer := newTailBuffer(8)
	if written, err := buffer.Write([]byte("012345")); err != nil || written != 6 {
		t.Fatalf("first write = %d, %v", written, err)
	}
	if written, err := buffer.Write([]byte("6789ABCDEF")); err != nil || written != 10 {
		t.Fatalf("second write = %d, %v", written, err)
	}
	if got := buffer.String(); got != "89ABCDEF" {
		t.Fatalf("tail = %q", got)
	}
}

func TestOSFileSystemCopyDoesNotOverwrite(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (OSFileSystem{}).Copy(source, destination); err == nil {
		t.Fatal("Copy succeeded over an existing backup")
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
}

func TestChecksumMismatch(t *testing.T) {
	updater := New(Hooks{Discover: func(context.Context) ([]Release, error) {
		release := testRelease("0.3.0", []byte("actual"))
		release.Assets["ruk-linux-x64"] = Asset{Name: "ruk-linux-x64", SHA256: strings.Repeat("0", 64), Size: 6}
		return []Release{release}, nil
	}, Download: func(context.Context, Asset) ([]byte, error) { return []byte("actual"), nil }})
	_, err := updater.Update(context.Background(), Options{
		Distribution: DistributionStandalone, CurrentVersion: "0.2.0", Platform: Platform{OS: "linux", Architecture: "x64"},
		Executable: filepath.Join(t.TempDir(), "ruk"),
	})
	if err == nil || !strings.Contains(err.Error(), "Checksum verification failed") {
		t.Fatalf("error = %v, want checksum failure", err)
	}
}

func TestStandaloneRollback(t *testing.T) {
	temporary := t.TempDir()
	executable := filepath.Join(temporary, "ruk")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	updater := New(Hooks{
		Discover: func(context.Context) ([]Release, error) { return []Release{testRelease("0.3.0", []byte("new"))}, nil },
		Download: func(context.Context, Asset) ([]byte, error) { return []byte("new"), nil },
		Run: func(context.Context, string, []string) (CommandResult, error) {
			return CommandResult{Stdout: "9.9.9\n"}, nil
		},
	})
	_, err := updater.Update(context.Background(), Options{
		Distribution: DistributionStandalone, CurrentVersion: "0.2.0", Platform: Platform{OS: "linux", Architecture: "x64"}, Executable: executable,
	})
	if err == nil || !strings.Contains(err.Error(), "failed its version check") {
		t.Fatalf("error = %v, want verification failure", err)
	}
	contents, readErr := os.ReadFile(executable)
	if readErr != nil || string(contents) != "old" {
		t.Fatalf("rollback contents = %q, read error = %v", contents, readErr)
	}
}

func TestPackageDelegation(t *testing.T) {
	var command string
	var args []string
	updater := New(Hooks{
		Discover: func(context.Context) ([]Release, error) {
			return []Release{testRelease("0.3.0", []byte("ignored"))}, nil
		},
		Run: func(_ context.Context, got string, gotArgs []string) (CommandResult, error) {
			command, args = got, append([]string(nil), gotArgs...)
			return CommandResult{}, nil
		},
	})
	result, err := updater.Update(context.Background(), Options{Distribution: DistributionPackage, CurrentVersion: "0.2.0", Installer: InstallerPNPM})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUpdated || command != "pnpm" || strings.Join(args, " ") != "add --global @xenoviz/ruk@0.3.0" {
		t.Fatalf("delegation = %+v %s %v", result, command, args)
	}
}

func TestDetectInstallerUsesDurablePackageMarker(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "ruk")
	marker := executable + ".ruk-distribution"
	if err := os.WriteFile(marker, []byte(`{"schemaVersion":1,"distribution":"package","installer":"pnpm"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installer, err := DetectInstaller(executable)
	if err != nil {
		t.Fatal(err)
	}
	if installer != InstallerPNPM {
		t.Fatalf("installer = %s, want pnpm", installer)
	}
	if err := os.WriteFile(marker, []byte(`{"schemaVersion":1,"distribution":"standalone","installer":"npm"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DetectInstaller(executable); err == nil {
		t.Fatal("invalid package marker was accepted")
	}
}

func TestDetectLinuxMuslUsesTheNativeLoader(t *testing.T) {
	called := false
	musl := detectMuslWith("linux", func(pattern string) ([]string, error) {
		called = true
		if pattern != "/lib/ld-musl-*.so.1" {
			t.Fatalf("loader pattern = %q", pattern)
		}
		return []string{"/lib/ld-musl-x86_64.so.1"}, nil
	})
	if !called || !musl {
		t.Fatalf("called=%v musl=%v, want native musl detection", called, musl)
	}
	called = false
	if detectMuslWith("darwin", func(string) ([]string, error) { called = true; return nil, nil }) || called {
		t.Fatal("non-Linux platform probed for a musl loader")
	}
}

func TestWindowsReplacementPlanUsesFileLockWithoutPIDPolling(t *testing.T) {
	helper, script, err := WindowsReplacementPlan(`C:\\bin\\ruk.exe`, `C:\\bin\\.ruk.exe.new`, "0.3.0-beta.1", 4242)
	if err != nil {
		t.Fatal(err)
	}
	if helper != `C:\\bin\\.ruk.exe.new.cmd` {
		t.Fatalf("helper = %q", helper)
	}
	for _, forbidden := range []string{"tasklist", "taskkill", "powershell", "4242"} {
		if strings.Contains(strings.ToLower(script), strings.ToLower(forbidden)) {
			t.Fatalf("replacement script contains unsafe %q: %s", forbidden, script)
		}
	}
	for _, required := range []string{"copy /Y", "move /Y", "timeout /t 1 /nobreak", "findstr /X"} {
		if !strings.Contains(script, required) {
			t.Fatalf("replacement script lacks %q: %s", required, script)
		}
	}
}

func TestWindowsReplacementPlanRejectsBatchInjection(t *testing.T) {
	for _, value := range []string{`C:\\bin\\ruk&evil.exe`, `C:\\bin\\ruk|evil.exe`, `C:\\bin\\ruk%PATH%.exe`, "0.3.0-beta.1\r\n"} {
		if _, _, err := WindowsReplacementPlan(value, `C:\\bin\\candidate.exe`, "0.3.0", 1); err == nil {
			t.Fatalf("unsafe replacement value %q was accepted", value)
		}
	}
}
