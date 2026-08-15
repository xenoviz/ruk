package distribution

import (
	"errors"
	"testing"
)

func TestResolveSupportedTargets(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		want   Mapping
	}{
		{
			name:   "linux amd64",
			target: Target{GOOS: "linux", GOARCH: "amd64"},
			want:   Mapping{AssetName: "ruk-linux-x64", PackageName: "@xenoviz/ruk-linux-x64", ExecutableName: "ruk"},
		},
		{
			name:   "linux arm64",
			target: Target{GOOS: "linux", GOARCH: "arm64"},
			want:   Mapping{AssetName: "ruk-linux-arm64", PackageName: "@xenoviz/ruk-linux-arm64", ExecutableName: "ruk"},
		},
		{
			name:   "linux amd64 musl",
			target: Target{GOOS: "linux", GOARCH: "amd64", Musl: true},
			want:   Mapping{AssetName: "ruk-linux-x64-musl", PackageName: "@xenoviz/ruk-linux-x64-musl", ExecutableName: "ruk"},
		},
		{
			name:   "darwin amd64",
			target: Target{GOOS: "darwin", GOARCH: "amd64"},
			want:   Mapping{AssetName: "ruk-macos-x64", PackageName: "@xenoviz/ruk-darwin-x64", ExecutableName: "ruk"},
		},
		{
			name:   "darwin arm64",
			target: Target{GOOS: "darwin", GOARCH: "arm64"},
			want:   Mapping{AssetName: "ruk-macos-arm64", PackageName: "@xenoviz/ruk-darwin-arm64", ExecutableName: "ruk"},
		},
		{
			name:   "windows amd64",
			target: Target{GOOS: "windows", GOARCH: "amd64"},
			want:   Mapping{AssetName: "ruk-windows-x64.exe", PackageName: "@xenoviz/ruk-win32-x64", ExecutableName: "ruk.exe"},
		},
		{
			name:   "windows arm64",
			target: Target{GOOS: "windows", GOARCH: "arm64"},
			want:   Mapping{AssetName: "ruk-windows-arm64.exe", PackageName: "@xenoviz/ruk-win32-arm64", ExecutableName: "ruk.exe"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Resolve(test.target)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("mapping = %#v, want %#v", got, test.want)
			}

			if asset, err := AssetName(test.target); err != nil || asset != test.want.AssetName {
				t.Fatalf("asset = %q, error = %v, want %q", asset, err, test.want.AssetName)
			}
			if packageName, err := PackageName(test.target); err != nil || packageName != test.want.PackageName {
				t.Fatalf("package = %q, error = %v, want %q", packageName, err, test.want.PackageName)
			}
			if executable, err := ExecutableName(test.target); err != nil || executable != test.want.ExecutableName {
				t.Fatalf("executable = %q, error = %v, want %q", executable, err, test.want.ExecutableName)
			}
		})
	}
}

func TestResolveRejectsUnsupportedTargets(t *testing.T) {
	tests := []struct {
		name   string
		target Target
	}{
		{name: "unsupported operating system", target: Target{GOOS: "freebsd", GOARCH: "amd64"}},
		{name: "unsupported architecture", target: Target{GOOS: "linux", GOARCH: "386"}},
		{name: "linux arm64 musl has no asset", target: Target{GOOS: "linux", GOARCH: "arm64", Musl: true}},
		{name: "darwin musl is invalid", target: Target{GOOS: "darwin", GOARCH: "arm64", Musl: true}},
		{name: "windows musl is invalid", target: Target{GOOS: "windows", GOARCH: "amd64", Musl: true}},
		{name: "empty target", target: Target{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Resolve(test.target)
			if !errors.Is(err, ErrUnsupportedTarget) {
				t.Fatalf("error = %v, want errors.Is(..., ErrUnsupportedTarget)", err)
			}
		})
	}
}

func TestDistributionMarkersAreExplicit(t *testing.T) {
	tests := []struct {
		name  string
		value Distribution
		valid bool
	}{
		{name: "package", value: DistributionPackage, valid: true},
		{name: "standalone", value: DistributionStandalone, valid: true},
		{name: "empty", value: "", valid: false},
		{name: "unknown", value: "auto", valid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.value.Valid(); got != test.valid {
				t.Fatalf("Valid() = %v, want %v", got, test.valid)
			}
		})
	}
}

func TestSupportedTargetsReturnsCopy(t *testing.T) {
	targets := SupportedTargets()
	if len(targets) != 7 {
		t.Fatalf("target count = %d, want 7", len(targets))
	}
	targets[0] = Target{}
	again := SupportedTargets()
	if again[0] == (Target{}) {
		t.Fatal("SupportedTargets returned mutable package state")
	}
}
