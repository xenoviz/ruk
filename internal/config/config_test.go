package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/config"
)

func TestLoadDefaultsWhenConfigIsMissing(t *testing.T) {
	t.Parallel()

	loaded, err := config.LoadWithEnvironment(t.TempDir(), map[string]string{})
	if err != nil {
		t.Fatalf("LoadWithEnvironment returned an error: %v", err)
	}
	if loaded.InstallCommand != nil || loaded.DependencyMode != nil || loaded.SharedCheckoutPolicy != config.Deny {
		t.Fatalf("loaded = %#v, want unset mode and deny policy defaults", loaded)
	}
}

func TestLoadRejectsMalformedJSONUnknownKeysAndNonObjectValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "malformed", body: "{", want: "Cannot read"},
		{name: "unknown", body: `{"mystery":true}`, want: "Unknown .rukrc.json option"},
		{name: "array", body: `[]`, want: "must contain a JSON object"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ".rukrc.json"), []byte(testCase.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.LoadWithEnvironment(root, map[string]string{})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestLoadValidatesCommandArrayAndDependencyMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "command scalar", body: `{"installCommand":"npm install"}`, want: "non-empty string array"},
		{name: "command empty", body: `{"installCommand":[]}`, want: "non-empty string array"},
		{name: "command empty part", body: `{"installCommand":["npm",""]}`, want: "non-empty string array"},
		{name: "mode invalid", body: `{"dependencyMode":"unsafe"}`, want: "managed\" or \"shared"},
		{name: "policy invalid", body: `{"sharedCheckoutPolicy":"sometimes"}`, want: "sharedCheckoutPolicy"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ".rukrc.json"), []byte(testCase.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.LoadWithEnvironment(root, map[string]string{})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want substring %q", err, testCase.want)
			}
		})
	}
}

func TestLoadAcceptsSharedCheckoutPolicies(t *testing.T) {
	t.Parallel()

	for _, policy := range []config.SharedCheckoutPolicy{config.Deny, config.Warn, config.Allow} {
		root := t.TempDir()
		body := `{"sharedCheckoutPolicy":"` + string(policy) + `"}`
		if err := os.WriteFile(filepath.Join(root, ".rukrc.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := config.LoadWithEnvironment(root, map[string]string{})
		if err != nil {
			t.Fatalf("policy %q returned an error: %v", policy, err)
		}
		if loaded.SharedCheckoutPolicy != policy {
			t.Fatalf("policy = %q, want %q", loaded.SharedCheckoutPolicy, policy)
		}
	}
}

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".rukrc.json"), []byte(`{"installCommand":["npm","ci"],"dependencyMode":"managed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadWithEnvironment(root, map[string]string{
		"RUK_INSTALL_COMMAND": `["bun","install"]`,
		"RUK_DEPENDENCY_MODE": "shared",
	})
	if err != nil {
		t.Fatalf("LoadWithEnvironment returned an error: %v", err)
	}
	if strings.Join(loaded.InstallCommand, " ") != "bun install" || loaded.DependencyMode == nil || *loaded.DependencyMode != config.Shared {
		t.Fatalf("loaded = %#v, want environment overrides", loaded)
	}
}

func TestLoadEnvironmentOverridesAreNullishAndPrecedenceCompatible(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".rukrc.json"), []byte(`{"installCommand":"invalid","dependencyMode":"unsafe"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadWithEnvironment(root, map[string]string{
		"RUK_INSTALL_COMMAND": `["bun","install"]`,
		"RUK_DEPENDENCY_MODE": "shared",
	})
	if err != nil {
		t.Fatalf("LoadWithEnvironment returned an error: %v", err)
	}
	if strings.Join(loaded.InstallCommand, " ") != "bun install" || loaded.DependencyMode == nil || *loaded.DependencyMode != config.Shared {
		t.Fatalf("loaded = %#v, want effective overrides", loaded)
	}

	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".rukrc.json"), []byte(`{"installCommand":["npm","ci"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err = config.LoadWithEnvironment(root, map[string]string{"RUK_INSTALL_COMMAND": "null"})
	if err != nil {
		t.Fatalf("LoadWithEnvironment(null command) returned an error: %v", err)
	}
	if strings.Join(loaded.InstallCommand, " ") != "npm ci" || loaded.DependencyMode != nil {
		t.Fatalf("loaded command = %#v, want file fallback", loaded.InstallCommand)
	}
}

func TestLoadEnvironmentCommandErrorsMatchContract(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"not-json", "[]", `[""]`} {
		_, err := config.LoadWithEnvironment(t.TempDir(), map[string]string{"RUK_INSTALL_COMMAND": value})
		if err == nil || !strings.Contains(err.Error(), "RUK_INSTALL_COMMAND") {
			t.Fatalf("value %q error = %v, want RUK_INSTALL_COMMAND validation", value, err)
		}
	}
	_, err := config.LoadWithEnvironment(t.TempDir(), map[string]string{"RUK_DEPENDENCY_MODE": ""})
	if err == nil || !strings.Contains(err.Error(), ".rukrc.json dependencyMode") || strings.Contains(err.Error(), "RUK_DEPENDENCY_MODE") {
		t.Fatalf("empty mode error = %v, want .rukrc.json dependencyMode validation", err)
	}
}

func TestDetectPackageManagerUsesCustomCommandBasenameAndManagedDefault(t *testing.T) {
	t.Parallel()

	manager, err := config.DetectPackageManager(t.TempDir(), config.Config{
		InstallCommand: []string{filepath.Join("tools", "custom.EXE"), "install"},
	})
	if err != nil {
		t.Fatalf("DetectPackageManager returned an error: %v", err)
	}
	if manager.Name != "custom" || manager.DependencyMode != config.Managed {
		t.Fatalf("manager = %#v, want custom managed manager", manager)
	}

	_, err = config.DetectPackageManager(t.TempDir(), config.Config{InstallCommand: []string{}})
	if err == nil || !strings.Contains(err.Error(), "installCommand cannot be empty") {
		t.Fatalf("empty custom command error = %v", err)
	}
}

func TestDetectPackageManagerReadsPackageManagerAndUsesExistenceSeam(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"fixture","packageManager":"@pnpm/exe@9.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var lookedUp string
	manager, err := config.DetectPackageManager(root, config.Config{}, func(name string) bool {
		lookedUp = name
		return true
	})
	if err != nil {
		t.Fatalf("DetectPackageManager returned an error: %v", err)
	}
	if lookedUp != "@pnpm/exe" || manager.Name != "@pnpm/exe" {
		t.Fatalf("lookup=%q manager=%#v, want package manager name", lookedUp, manager)
	}
	if strings.Join(manager.Command, " ") != "@pnpm/exe install" || manager.DependencyMode != config.Managed {
		t.Fatalf("manager = %#v, want managed install command", manager)
	}
}

func TestDetectPackageManagerUsesDeterministicLockfileCommandsAndModes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		files   []string
		command string
		mode    config.DependencyMode
	}{
		{name: "bun", files: []string{"bun.lock", "bun.lockb", "pnpm-lock.yaml", "yarn.lock", "package-lock.json"}, command: "bun install --frozen-lockfile", mode: config.Shared},
		{name: "pnpm", files: []string{"pnpm-lock.yaml"}, command: "pnpm install --frozen-lockfile", mode: config.Shared},
		{name: "yarn", files: []string{"yarn.lock"}, command: "yarn install --frozen-lockfile", mode: config.Managed},
		{name: "npm ci", files: []string{"package-lock.json"}, command: "npm ci", mode: config.Managed},
		{name: "npm install", files: nil, command: "npm install", mode: config.Managed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			for _, file := range testCase.files {
				if err := os.WriteFile(filepath.Join(root, file), []byte(""), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := config.DetectPackageManager(root, config.Config{}, func(string) bool { return true })
			if err != nil {
				t.Fatalf("DetectPackageManager returned an error: %v", err)
			}
			if strings.Join(manager.Command, " ") != testCase.command || manager.DependencyMode != testCase.mode {
				t.Fatalf("manager = %#v, want command %q mode %q", manager, testCase.command, testCase.mode)
			}
		})
	}
}

func TestDetectPackageManagerReportsMissingCommandFromInjectedSeam(t *testing.T) {
	t.Parallel()

	_, err := config.DetectPackageManager(t.TempDir(), config.Config{}, func(string) bool { return false })
	if err == nil || !strings.Contains(err.Error(), "npm is required but was not found on PATH") {
		t.Fatalf("missing command error = %v", err)
	}
}

func TestDetectPackageManagerHonorsExplicitDependencyMode(t *testing.T) {
	t.Parallel()

	mode := config.Managed
	manager, err := config.DetectPackageManager(t.TempDir(), config.Config{
		DependencyMode: &mode,
	}, func(string) bool { return true })
	if err != nil {
		t.Fatalf("DetectPackageManager returned an error: %v", err)
	}
	if manager.Name != "npm" || manager.DependencyMode != config.Managed {
		t.Fatalf("manager = %#v, want explicit managed mode", manager)
	}
}
