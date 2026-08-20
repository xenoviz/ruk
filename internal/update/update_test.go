package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
		return []Release{
			testRelease("0.4.0", []byte("stable")),
			testRelease("0.4.0-alpha.1", []byte("alpha")),
			testRelease("0.3.0-beta.2", []byte("beta")),
			testRelease("0.2.9", []byte("stable")),
		}, nil
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

func TestExplicitPrereleaseOptInCanChangeChannel(t *testing.T) {
	updater := New(Hooks{Discover: func(context.Context) ([]Release, error) {
		return []Release{
			testRelease("0.4.0-alpha.1", []byte("alpha")),
			testRelease("0.3.0-beta.2", []byte("beta")),
		}, nil
	}})
	result, err := updater.Update(context.Background(), Options{
		Distribution: DistributionPackage, CurrentVersion: "0.3.0-beta.1", CheckOnly: true, AllowPrerelease: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.LatestVersion != "0.4.0-alpha.1" {
		t.Fatalf("explicit prerelease result = %+v", result)
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
	result, err := updater.Update(context.Background(), Options{Distribution: DistributionPackage, CurrentVersion: "0.2.0", AllowPrerelease: true, Platform: Platform{OS: "linux"}})
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
	if runtime.GOOS == "windows" {
		t.Skip("POSIX replacement relies on rename-over-existing semantics")
	}
	temporary := t.TempDir()
	executable := filepath.Join(temporary, "ruk")
	if err := os.WriteFile(executable, []byte("old"), 0o701); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(temporary, "ruk.new")
	if err := os.WriteFile(candidate, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := replacePOSIX(OSFileSystem{}, func(context.Context, string, []string) (CommandResult, error) {
		return CommandResult{Stdout: "9.9.9\n"}, nil
	}, context.Background(), executable, candidate, "0.3.0")
	if err == nil || !strings.Contains(err.Error(), "failed its version check") {
		t.Fatalf("error = %v, want verification failure", err)
	}
	contents, readErr := os.ReadFile(executable)
	if readErr != nil || string(contents) != "old" {
		t.Fatalf("rollback contents = %q, read error = %v", contents, readErr)
	}
	metadata, statErr := os.Stat(executable)
	if statErr != nil {
		t.Fatalf("rollback stat error = %v", statErr)
	}
	if metadata.Mode().Perm() != 0o701 {
		t.Fatalf("rollback mode = %o, want 701", metadata.Mode().Perm())
	}
}

func TestStandaloneUpdateUsesResolvedEntrypointTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX replacement relies on rename-over-existing semantics")
	}
	temporary := t.TempDir()
	target := filepath.Join(temporary, "ruk-target")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(temporary, "ruk")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	updater := New(Hooks{
		Discover: func(context.Context) ([]Release, error) {
			return []Release{testRelease("0.3.0", []byte("new"))}, nil
		},
		Download: func(context.Context, Asset) ([]byte, error) {
			return []byte("new"), nil
		},
		Run: func(context.Context, string, []string) (CommandResult, error) {
			return CommandResult{Stdout: "0.3.0\n"}, nil
		},
	})
	result, err := updater.Update(context.Background(), Options{
		Distribution: DistributionStandalone, CurrentVersion: "0.2.0",
		Platform: Platform{OS: "linux", Architecture: "x64"}, Entrypoint: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUpdated {
		t.Fatalf("standalone update status = %s, want %s", result.Status, StatusUpdated)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "new" {
		t.Fatalf("resolved target contents = %q, read error = %v", contents, err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("entrypoint symlink target = %q, want %q (error %v)", resolved, target, err)
	}
	resolvedInfo, resolvedErr := os.Stat(resolved)
	targetInfo, targetErr := os.Stat(target)
	if resolvedErr != nil || targetErr != nil || !os.SameFile(resolvedInfo, targetInfo) {
		t.Fatalf("entrypoint symlink target = %q, want same file as %q (errors %v, %v)", resolved, target, resolvedErr, targetErr)
	}
}

type backupChmodFailureFileSystem struct {
	OSFileSystem
	removed []string
}

func (fileSystem *backupChmodFailureFileSystem) Chmod(name string, mode fs.FileMode) error {
	if strings.Contains(filepath.Base(name), ".ruk-backup-") {
		return fmt.Errorf("backup permission restore failed")
	}
	return fileSystem.OSFileSystem.Chmod(name, mode)
}

func (fileSystem *backupChmodFailureFileSystem) Remove(name string) error {
	fileSystem.removed = append(fileSystem.removed, name)
	return fileSystem.OSFileSystem.Remove(name)
}

func TestStandaloneRollbackCleansBackupWhenPermissionRestoreFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX replacement relies on rename-over-existing semantics")
	}
	temporary := t.TempDir()
	executable := filepath.Join(temporary, "ruk")
	candidate := filepath.Join(temporary, "ruk.new")
	if err := os.WriteFile(executable, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	fileSystem := &backupChmodFailureFileSystem{}
	err := replacePOSIX(fileSystem, func(context.Context, string, []string) (CommandResult, error) {
		return CommandResult{Stdout: "0.3.0\n"}, nil
	}, context.Background(), executable, candidate, "0.3.0")
	if err == nil || !strings.Contains(err.Error(), "backup permission restore failed") {
		t.Fatalf("error = %v, want backup permission failure", err)
	}
	if len(fileSystem.removed) != 1 || !strings.Contains(filepath.Base(fileSystem.removed[0]), ".ruk-backup-") {
		t.Fatalf("removed backup paths = %v", fileSystem.removed)
	}
	entries, readErr := os.ReadDir(temporary)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".ruk-backup-") {
			t.Fatalf("backup artifact remained after chmod failure: %s", entry.Name())
		}
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
	result, err := updater.Update(context.Background(), Options{Distribution: DistributionPackage, CurrentVersion: "0.2.0", Installer: InstallerPNPM, Platform: Platform{OS: "linux"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUpdated || command != "pnpm" || strings.Join(args, " ") != "add --global @xenoviz/ruk@0.3.0" {
		t.Fatalf("delegation = %+v %s %v", result, command, args)
	}
}

func TestWindowsPackageUpdateReportsScheduledReplacement(t *testing.T) {
	updater := New(Hooks{
		Discover: func(context.Context) ([]Release, error) {
			return []Release{testRelease("0.3.0", []byte("ignored"))}, nil
		},
		Run: func(_ context.Context, command string, args []string) (CommandResult, error) {
			if command != "npm" || strings.Join(args, " ") != "install --global @xenoviz/ruk@0.3.0" {
				t.Fatalf("unexpected package command: %s %s", command, args)
			}
			return CommandResult{}, nil
		},
	})
	result, err := updater.Update(context.Background(), Options{
		Distribution: DistributionPackage, CurrentVersion: "0.2.0",
		Installer: InstallerNPM, Platform: Platform{OS: "windows"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusScheduled {
		t.Fatalf("Windows package update status = %s, want %s", result.Status, StatusScheduled)
	}
}

func TestPackageUpdatePassesHumanAndMachineReadableCommandIO(t *testing.T) {
	var seen []CommandIO
	updater := New(Hooks{
		Discover: func(context.Context) ([]Release, error) {
			return []Release{testRelease("0.3.0", []byte("ignored"))}, nil
		},
		RunWithIO: func(_ context.Context, command string, args []string, commandIO CommandIO) (CommandResult, error) {
			if command != "npm" || strings.Join(args, " ") != "install --global @xenoviz/ruk@0.3.0" {
				return CommandResult{}, fmt.Errorf("unexpected package command: %s %s", command, args)
			}
			seen = append(seen, commandIO)
			return CommandResult{}, nil
		},
	})
	humanIn, humanOut, humanErr := strings.NewReader("confirm\n"), new(strings.Builder), new(strings.Builder)
	if _, err := updater.Update(context.Background(), Options{
		Distribution: DistributionPackage, CurrentVersion: "0.2.0",
		Stdin: humanIn, Stdout: humanOut, Stderr: humanErr,
	}); err != nil {
		t.Fatal(err)
	}
	machineIn, machineOut, machineErr := strings.NewReader("should-not-be-forwarded"), new(strings.Builder), new(strings.Builder)
	if _, err := updater.Update(context.Background(), Options{
		Distribution: DistributionPackage, CurrentVersion: "0.2.0",
		Stdin: machineIn, Stdout: machineOut, Stderr: machineErr, MachineReadable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("command IO calls = %d, want 2", len(seen))
	}
	if seen[0].Stdin != humanIn || seen[0].Stdout != humanOut || seen[0].Stderr != humanErr || seen[0].MachineReadable {
		t.Fatalf("human command IO = %#v", seen[0])
	}
	if seen[1].Stdin != nil || seen[1].Stdout != nil || seen[1].Stderr != nil || !seen[1].MachineReadable {
		t.Fatalf("machine command IO = %#v", seen[1])
	}
}

func TestCommandSpecForPlatformUsesCOMSPECForWindowsPackageShims(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		command string
		args    []string
		want    string
		wrapped bool
	}{
		{
			name:    "npm bare shim",
			goos:    "windows",
			command: "npm",
			args:    []string{"install", "--global", "@xenoviz/ruk@0.3.0"},
			want:    `call "npm" "install" "--global" "@xenoviz/ruk@0.3.0"`,
			wrapped: true,
		},
		{
			name:    "explicit batch path",
			goos:    "windows",
			command: `C:\Program Files\nodejs\npm.cmd`,
			args:    []string{"install"},
			want:    `call "C:\Program Files\nodejs\npm.cmd" "install"`,
			wrapped: true,
		},
		{
			name:    "native executable stays direct",
			goos:    "windows",
			command: "bun",
			args:    []string{"add", "--global", "@xenoviz/ruk@0.3.0"},
			want:    "add --global @xenoviz/ruk@0.3.0",
		},
		{
			name:    "non Windows stays direct",
			goos:    "linux",
			command: "npm.cmd",
			args:    []string{"install"},
			want:    "install",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, err := commandSpecForPlatform(test.goos, `C:\Windows\System32\cmd.exe`, test.command, test.args)
			if err != nil {
				t.Fatal(err)
			}
			if test.wrapped {
				if spec.command != `C:\Windows\System32\cmd.exe` || len(spec.args) != 5 || spec.args[0] != `C:\Windows\System32\cmd.exe` || spec.args[1] != "/d" || spec.args[2] != "/s" || spec.args[3] != "/c" || spec.args[4] != test.want || spec.cmdLine == "" {
					t.Fatalf("wrapped command = %#v", spec)
				}
				return
			}
			if spec.command != test.command || strings.Join(spec.args, " ") != test.want || spec.cmdLine != "" {
				t.Fatalf("direct command = %#v", spec)
			}
		})
	}
}

func TestCommandSpecForPlatformRejectsWindowsShellExpansion(t *testing.T) {
	for _, value := range []string{"%PATH%", `quoted"value`, "line\nvalue", "bang!"} {
		if _, err := commandSpecForPlatform("windows", "cmd.exe", "npm", []string{value}); err == nil {
			t.Fatalf("unsafe token %q was accepted", value)
		}
	}
}

func TestBunInstallerCommandTrustsNativePostinstall(t *testing.T) {
	command, args, err := InstallerCommand(InstallerBun, "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if command != "bun" || strings.Join(args, " ") != "add --global --trust @xenoviz/ruk@0.3.0" {
		t.Fatalf("bun update command = %s %v", command, args)
	}
}

type updateRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper updateRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func readyManifestBytes(version string) []byte {
	assetNames := []string{
		"ruk-linux-x64", "ruk-linux-arm64", "ruk-linux-x64-musl", "ruk-macos-x64",
		"ruk-macos-arm64", "ruk-windows-x64.exe", "ruk-windows-arm64.exe",
	}
	assets := make(map[string]Asset, len(assetNames))
	for _, name := range assetNames {
		assets[name] = Asset{Name: name, SHA256: strings.Repeat("a", 64), Size: 1}
	}
	manifest, err := json.Marshal(Manifest{
		SchemaVersion: 1, Repository: Repository, Version: version,
		Package: ManifestPackage{Name: PackageName, Version: version}, Assets: assets,
	})
	if err != nil {
		panic(err)
	}
	return manifest
}

func TestHTTPDiscoveryPagesUntilReadyRelease(t *testing.T) {
	readyVersion := "0.3.0"
	pageOne := make([]map[string]any, releasesPerPage)
	for index := range pageOne {
		pageOne[index] = map[string]any{"tag_name": fmt.Sprintf("not-a-version-%d", index)}
	}
	readyAssets := make([]map[string]string, 0, 8)
	for _, name := range append([]string{"ruk-release.json"}, "ruk-linux-x64", "ruk-linux-arm64", "ruk-linux-x64-musl", "ruk-macos-x64", "ruk-macos-arm64", "ruk-windows-x64.exe", "ruk-windows-arm64.exe") {
		readyAssets = append(readyAssets, map[string]string{
			"name":                 name,
			"browser_download_url": "https://github.com/xenoviz/ruk/releases/download/v" + readyVersion + "/" + name,
		})
	}
	pageTwo := []map[string]any{{"tag_name": "v" + readyVersion, "assets": readyAssets}}
	pageRequests := make([]int, 0, 2)
	client := &http.Client{Transport: updateRoundTripper(func(request *http.Request) (*http.Response, error) {
		var body []byte
		switch request.URL.Query().Get("page") {
		case "1":
			pageRequests = append(pageRequests, 1)
			body, _ = json.Marshal(pageOne)
		case "2":
			pageRequests = append(pageRequests, 2)
			body, _ = json.Marshal(pageTwo)
		default:
			return nil, fmt.Errorf("unexpected page %q", request.URL.Query().Get("page"))
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	updater := New(Hooks{HTTPClient: client, Download: func(context.Context, Asset) ([]byte, error) {
		return readyManifestBytes(readyVersion), nil
	}})
	result, err := updater.discoverHTTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Version != readyVersion {
		t.Fatalf("discovered releases = %+v", result)
	}
	if fmt.Sprint(pageRequests) != "[1 2]" {
		t.Fatalf("pages requested = %v", pageRequests)
	}
}

func TestGitHubAPIHeadersOptionalToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	request, err := http.NewRequest(http.MethodGet, ReleasesURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyGitHubAPIHeaders(request)
	if request.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization = %q, want empty", request.Header.Get("Authorization"))
	}
	if request.Header.Get("Accept") != "application/vnd.github+json" {
		t.Fatalf("Accept = %q", request.Header.Get("Accept"))
	}
	if request.Header.Get("User-Agent") != defaultUserAgent {
		t.Fatalf("User-Agent = %q", request.Header.Get("User-Agent"))
	}

	t.Setenv("GITHUB_TOKEN", "from-github")
	t.Setenv("GH_TOKEN", "from-gh")
	request, err = http.NewRequest(http.MethodGet, ReleasesURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyGitHubAPIHeaders(request)
	if got := request.Header.Get("Authorization"); got != "Bearer from-gh" {
		t.Fatalf("Authorization = %q, want Bearer from-gh", got)
	}

	t.Setenv("GH_TOKEN", "")
	request, err = http.NewRequest(http.MethodGet, ReleasesURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyGitHubAPIHeaders(request)
	if got := request.Header.Get("Authorization"); got != "Bearer from-github" {
		t.Fatalf("Authorization = %q, want Bearer from-github", got)
	}
}

func TestHTTPDiscoveryRejectsMalformedReleasePage(t *testing.T) {
	client := &http.Client{Transport: updateRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("{"))}, nil
	})}
	_, err := New(Hooks{HTTPClient: client}).discoverHTTP(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid release list") {
		t.Fatalf("malformed page error = %v", err)
	}
}

func TestDefaultHTTPClientAppliesTimeout(t *testing.T) {
	client := defaultHTTPClient(17 * time.Second)
	if client == nil || client.Timeout != 17*time.Second {
		t.Fatalf("default client = %#v, want 17s timeout", client)
	}
}

func TestDownloadFuncReadsStreamedBodyAfterDoHTTPReturns(t *testing.T) {
	chunk := bytes.Repeat([]byte{0xab}, 256*1024)
	const chunks = 16
	want := bytes.Repeat(chunk, chunks)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.WriteHeader(http.StatusOK)
		flusher, ok := writer.(http.Flusher)
		if !ok {
			http.Error(writer, "response cannot flush", http.StatusInternalServerError)
			return
		}
		flusher.Flush()
		for range chunks {
			if _, err := writer.Write(chunk); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	t.Cleanup(server.Close)

	got, err := New(Hooks{}).downloadFunc()(context.Background(), Asset{Name: "ruk-linux-x64", URL: server.URL})
	if err != nil {
		t.Fatalf("downloadFunc returned an error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("downloaded %d bytes, want %d streamed bytes", len(got), len(want))
	}
}

func TestDoHTTPTimeoutInterruptsUnreadBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := New(Hooks{}).doHTTP(context.Background(), request, 300*time.Millisecond)
	if err == nil {
		_, err = io.ReadAll(response.Body)
		response.Body.Close()
	}
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("hung body completed without a timeout")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("client timeout took %s, want under 2s", elapsed)
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
	helper, script, err := WindowsReplacementPlan(`C:\bin\ruk.exe`, `C:\bin\.ruk.exe.new`, "0.3.0-beta.1", 4242)
	if err != nil {
		t.Fatal(err)
	}
	if helper != `C:\bin\.ruk.exe.new.cmd` {
		t.Fatalf("helper = %q", helper)
	}
	for _, forbidden := range []string{"tasklist", "taskkill", "powershell", "4242"} {
		if strings.Contains(strings.ToLower(script), strings.ToLower(forbidden)) {
			t.Fatalf("replacement script contains unsafe %q: %s", forbidden, script)
		}
	}
	for _, required := range []string{"copy /Y", "move /Y", "timeout /t 1 /nobreak", "findstr /X", "waitAttempts=0", "waitAttempts+=1", "GEQ 120 goto wait_failed", "rollbackAttempts=0", "rollbackAttempts+=1", "if not errorlevel 1 goto rollback_succeeded", "GEQ 120 goto rollback_failed"} {
		if !strings.Contains(script, required) {
			t.Fatalf("replacement script lacks %q: %s", required, script)
		}
	}
	rollback := strings.Index(script, ":rollback")
	failure := strings.Index(script, ":rollback_failed")
	if rollback < 0 || failure < 0 || failure <= rollback {
		t.Fatalf("replacement script has malformed rollback labels: %s", script)
	}
	rollbackBlock := script[rollback:failure]
	if !strings.Contains(rollbackBlock, "if not errorlevel 1 goto rollback_succeeded") || !strings.Contains(rollbackBlock, "GEQ 120 goto rollback_failed") || !strings.Contains(rollbackBlock, "goto rollback") {
		t.Fatalf("rollback is not bounded and retryable: %s", rollbackBlock)
	}
	if strings.Contains(script[rollback:], "del /Q \"C:\\bin\\.ruk.exe.new.backup\"") {
		t.Fatalf("failure path deletes the known-good backup: %s", script)
	}
	if !strings.Contains(script, ":rollback_failed") {
		t.Fatalf("replacement script lacks bounded rollback failure label: %s", script)
	}
	wait := strings.Index(script, ":wait")
	waitFailed := strings.Index(script, ":wait_failed")
	rollbackStart := strings.Index(script, ":rollback")
	if wait < 0 || waitFailed <= wait || rollbackStart <= waitFailed {
		t.Fatalf("replacement script has malformed bounded wait labels: %s", script)
	}
	waitBlock := script[wait:waitFailed]
	if !strings.Contains(waitBlock, "if %waitAttempts% GEQ 120 goto wait_failed") || !strings.Contains(waitBlock, "goto wait") {
		t.Fatalf("initial replacement wait is not bounded and retryable: %s", waitBlock)
	}
	if strings.Contains(script[waitFailed:rollbackStart], "del /Q") {
		t.Fatalf("initial wait failure deletes recovery artifacts: %s", script[waitFailed:rollbackStart])
	}
	if strings.Index(script, "set /A rollbackAttempts=0") > strings.Index(script, ":wait") {
		t.Fatalf("rollback counter is initialized after the wait loop: %s", script)
	}
}

func TestWindowsReplacementPlanRejectsBatchInjection(t *testing.T) {
	for _, value := range []string{`C:\bin\ruk&evil.exe`, `C:\bin\ruk|evil.exe`, `C:\bin\ruk%PATH%.exe`, "0.3.0-beta.1\r\n"} {
		if _, _, err := WindowsReplacementPlan(value, `C:\bin\candidate.exe`, "0.3.0", 1); err == nil {
			t.Fatalf("unsafe replacement value %q was accepted", value)
		}
	}
}
