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
