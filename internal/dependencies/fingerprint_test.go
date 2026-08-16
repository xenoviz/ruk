package dependencies

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDependencyFilesKeepsNestedRootsAndDependencyConfig(t *testing.T) {
	paths := []string{
		"source.ts",
		"package.json",
		`packages\api\package.json`,
		"packages/api/pnpm-lock.yaml",
		"packages/api/.npmrc",
		"patches/fix.patch",
		"packages/ui/patches/theme.patch",
		"not-patches/source.ts",
		"package.json",
	}
	want := []string{
		"package.json",
		"packages/api/.npmrc",
		"packages/api/package.json",
		"packages/api/pnpm-lock.yaml",
		"packages/ui/patches/theme.patch",
		"patches/fix.patch",
	}
	if got := DependencyFiles(paths); !reflect.DeepEqual(got, want) {
		t.Fatalf("DependencyFiles() = %#v, want %#v", got, want)
	}
}

func TestDependencyFingerprintTracksOnlyDependencyInputs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "package.json"), `{"name":"root"}`)
	writeFile(t, filepath.Join(root, "packages", "api", "package.json"), `{"name":"api","version":"1"}`)
	writeFile(t, filepath.Join(root, "bun.lock"), "lock-v1\n")
	writeFile(t, filepath.Join(root, ".rukrc.json"), `{"dependencyMode":"managed"}`)
	writeFile(t, filepath.Join(root, "source.ts"), "const value = 1\n")
	input := SourceFingerprintInput{
		Root:  root,
		Files: []string{"source.ts", "package.json", "packages/api/package.json", "bun.lock", ".rukrc.json"},
		Manager: PackageManager{
			Name: "bun", Version: "1.3.14", Command: []string{"bun", "install", "--frozen-lockfile"}, DependencyMode: "managed",
		},
		Runtime: RuntimeIdentity{Platform: "test-os", Architecture: "test-arch", Runtime: "bun", Version: "1.3.14", NativeABI: "1"},
	}
	first, err := DependencyFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(root, "source.ts"), "const value = 2\n")
	sourceChanged, err := DependencyFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	if sourceChanged.Fingerprint != first.Fingerprint {
		t.Fatal("ordinary source changed the dependency fingerprint")
	}

	writeFile(t, filepath.Join(root, "packages", "api", "package.json"), `{"name":"api","version":"2"}`)
	manifestChanged, err := DependencyFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	if manifestChanged.Fingerprint == first.Fingerprint {
		t.Fatal("nested package manifest did not change the fingerprint")
	}

	writeFile(t, filepath.Join(root, "bun.lock"), "lock-v2\n")
	lockChanged, err := DependencyFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	if lockChanged.Fingerprint == manifestChanged.Fingerprint {
		t.Fatal("lockfile did not change the fingerprint")
	}

	writeFile(t, filepath.Join(root, "bun.lock"), "lock-v2\r\n")
	lineEndingsChanged, err := DependencyFingerprint(input)
	if err != nil {
		t.Fatal(err)
	}
	if lineEndingsChanged.Fingerprint != lockChanged.Fingerprint {
		t.Fatal("CRLF normalization changed the text fingerprint")
	}

	shared := input
	shared.Manager.DependencyMode = "shared"
	sharedDetails, err := DependencyFingerprint(shared)
	if err != nil {
		t.Fatal(err)
	}
	if sharedDetails.Fingerprint == lineEndingsChanged.Fingerprint {
		t.Fatal("dependency mode did not change the fingerprint")
	}
}

func TestDependencyFingerprintTracksManagerAndRuntimeIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "package.json"), `{"name":"fixture"}`)
	base := SourceFingerprintInput{
		Root: root, Files: []string{"package.json"},
		Manager: PackageManager{Name: "npm", Version: "10.0.0", Command: []string{"npm", "install"}, DependencyMode: "managed"},
		Runtime: RuntimeIdentity{Runtime: "node", Version: "22.0.0", NativeABI: "127"},
	}
	first, err := DependencyFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	managerChanged := base
	managerChanged.Manager.Version = "11.0.0"
	second, err := DependencyFingerprint(managerChanged)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("package-manager version did not change dependency fingerprint")
	}
	runtimeChanged := base
	runtimeChanged.Runtime.NativeABI = "128"
	third, err := DependencyFingerprint(runtimeChanged)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == third.Fingerprint {
		t.Fatal("runtime ABI did not change dependency fingerprint")
	}
}

func TestProjectionFingerprintTracksNestedAndSymlinkTargetChanges(t *testing.T) {
	root := t.TempDir()
	projection := filepath.Join(root, "packages", "api", "node_modules")
	if err := os.MkdirAll(projection, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(projection, "direct", "index.js"), "direct")
	target := filepath.Join(root, "store", "package")
	writeFile(t, filepath.Join(target, "index.js"), "one")
	link := filepath.Join(projection, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	before, err := ProjectionFingerprint(root, []string{"packages/api/node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, "index.js"), "two")
	after, err := ProjectionFingerprint(root, []string{"packages/api/node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("symlink target change did not change projection fingerprint")
	}
	if !ProjectionIntegrityValid(root, []string{"packages/api/node_modules"}, after) {
		t.Fatal("matching projection fingerprint was rejected")
	}
}

func TestProjectionFingerprintRejectsLexicalAndSymlinkedAncestors(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".", "..", "../node_modules", "node_modules/../../outside"} {
		if _, err := ProjectionFingerprint(root, []string{relative}); err == nil {
			t.Errorf("ProjectionFingerprint(%q) succeeded, want containment error", relative)
		}
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(root, "packages", "app")
	if err := os.MkdirAll(filepath.Dir(ancestor), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := ProjectionFingerprint(root, []string{"packages/app/node_modules"}); err == nil || !strings.Contains(err.Error(), "symlinked ancestor") {
		t.Fatalf("symlinked ancestor error = %v", err)
	}
}

func TestProjectionIntegrityFailsClosedOnCorruption(t *testing.T) {
	root := t.TempDir()
	projection := filepath.Join(root, "node_modules")
	writeFile(t, filepath.Join(projection, "index.js"), "one")
	fingerprint, err := ProjectionFingerprint(root, []string{"node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		projections []string
		expected    string
	}{
		{name: "wrong fingerprint", projections: []string{"node_modules"}, expected: strings.Repeat("0", 64)},
		{name: "missing projection", projections: []string{"missing"}, expected: fingerprint},
		{name: "empty expected", projections: []string{"node_modules"}, expected: ""},
		{name: "empty projections", projections: nil, expected: fingerprint},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if ProjectionIntegrityValid(root, test.projections, test.expected) {
				t.Fatal("corrupt projection was accepted")
			}
		})
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
