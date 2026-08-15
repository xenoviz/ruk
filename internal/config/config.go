// Package config loads the repository-local Ruk configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DependencyMode controls how dependencies are prepared for a workspace.
type DependencyMode string

const (
	// Managed gives each workspace its own writable dependency installation.
	Managed DependencyMode = "managed"
	// Shared permits a supported package manager to use immutable shared storage.
	Shared DependencyMode = "shared"
)

// SharedCheckoutPolicy controls how commands handle an active shared primary
// checkout.
type SharedCheckoutPolicy string

const (
	Deny  SharedCheckoutPolicy = "deny"
	Warn  SharedCheckoutPolicy = "warn"
	Allow SharedCheckoutPolicy = "allow"
)

// Config is the validated repository configuration.
//
// InstallCommand is nil when no command was configured. A nil command lets the
// dependency layer detect the package manager from the repository. A nil
// DependencyMode means no mode was configured, allowing manager detection to
// choose its supported default.
type Config struct {
	InstallCommand       []string             `json:"installCommand"`
	DependencyMode       *DependencyMode      `json:"dependencyMode"`
	SharedCheckoutPolicy SharedCheckoutPolicy `json:"sharedCheckoutPolicy"`
}

// Load reads .rukrc.json from root and applies the Ruk environment overrides.
func Load(root string) (Config, error) {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		for index := 0; index < len(entry); index++ {
			if entry[index] == '=' {
				environment[entry[:index]] = entry[index+1:]
				break
			}
		}
	}
	return LoadWithEnvironment(root, environment)
}

// LoadWithEnvironment is Load with an explicit environment snapshot. It is
// useful to callers that already own an environment snapshot and to tests that
// need deterministic override behavior. A key's presence, including an empty
// value, is significant for dependency mode.
func LoadWithEnvironment(root string, environment map[string]string) (Config, error) {
	file := filepath.Join(root, ".rukrc.json")
	fileConfig, exists, err := readFileConfig(file)
	if err != nil {
		return Config{}, err
	}
	if !exists {
		fileConfig = map[string]json.RawMessage{}
	}

	if unknown := unknownKeys(fileConfig); len(unknown) > 0 {
		return Config{}, fmt.Errorf("Unknown .rukrc.json option%s: %s", plural(len(unknown)), joinKeys(unknown))
	}

	var environmentCommand []string
	if value, ok := environment["RUK_INSTALL_COMMAND"]; ok && value != "" {
		var err error
		environmentCommand, err = commandFromEnvironment(value)
		if err != nil {
			return Config{}, err
		}
	}

	// JSON null is intentionally treated as no override, matching the
	// TypeScript contract's nullish fallback to the file configuration. The
	// file value is validated only when it is the effective value, so a valid
	// environment override masks an invalid file value.
	command := environmentCommand
	if command == nil {
		var err error
		command, err = commandFromRaw(fileConfig["installCommand"], ".rukrc.json installCommand")
		if err != nil {
			return Config{}, err
		}
	}

	var mode *DependencyMode
	if value, ok := environment["RUK_DEPENDENCY_MODE"]; ok {
		var err error
		source := ".rukrc.json dependencyMode"
		if value != "" {
			source = "RUK_DEPENDENCY_MODE"
		}
		mode, err = modeFromValue(value, source)
		if err != nil {
			return Config{}, err
		}
	} else {
		var err error
		mode, err = modeFromRaw(fileConfig["dependencyMode"], ".rukrc.json dependencyMode")
		if err != nil {
			return Config{}, err
		}
	}

	policy, err := policyFromRaw(fileConfig["sharedCheckoutPolicy"])
	if err != nil {
		return Config{}, err
	}
	return Config{InstallCommand: command, DependencyMode: mode, SharedCheckoutPolicy: policy}, nil
}

func readFileConfig(file string) (map[string]json.RawMessage, bool, error) {
	contents, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("Cannot read %s: %w", file, err)
	}

	var value any
	if err := json.Unmarshal(contents, &value); err != nil {
		return nil, false, fmt.Errorf("Cannot read %s: %w", file, err)
	}
	if value == nil {
		return nil, false, errors.New(".rukrc.json must contain a JSON object")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, false, errors.New(".rukrc.json must contain a JSON object")
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(contents, &object); err != nil {
		return nil, false, fmt.Errorf("Cannot read %s: %w", file, err)
	}
	return object, true, nil
}

func unknownKeys(object map[string]json.RawMessage) []string {
	unknown := make([]string, 0)
	for key := range object {
		if key != "installCommand" && key != "dependencyMode" && key != "sharedCheckoutPolicy" {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

func commandFromRaw(raw json.RawMessage, source string) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be a non-empty string array", source)
	}
	return validateCommand(value, source)
}

func commandFromEnvironment(value string) ([]string, error) {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, errors.New("RUK_INSTALL_COMMAND must be a JSON string array")
	}
	return validateCommand(parsed, "RUK_INSTALL_COMMAND")
}

func validateCommand(value any, source string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	parts, ok := value.([]any)
	if !ok || len(parts) == 0 {
		return nil, fmt.Errorf("%s must be a non-empty string array", source)
	}
	command := make([]string, len(parts))
	for index, part := range parts {
		text, ok := part.(string)
		if !ok || text == "" {
			return nil, fmt.Errorf("%s must be a non-empty string array", source)
		}
		command[index] = text
	}
	return command, nil
}

func modeFromRaw(raw json.RawMessage, source string) (*DependencyMode, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%s must be either \"managed\" or \"shared\"", source)
	}
	if value == nil {
		return nil, nil
	}
	return modeFromValue(value, source)
}

func modeFromValue(value any, source string) (*DependencyMode, error) {
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be either \"managed\" or \"shared\"", source)
	}
	switch DependencyMode(text) {
	case Managed, Shared:
		mode := DependencyMode(text)
		return &mode, nil
	default:
		return nil, fmt.Errorf("%s must be either \"managed\" or \"shared\"", source)
	}
}

func policyFromRaw(raw json.RawMessage) (SharedCheckoutPolicy, error) {
	if len(raw) == 0 {
		return Deny, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", invalidPolicyError()
	}
	if value == nil {
		return Deny, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", invalidPolicyError()
	}
	switch SharedCheckoutPolicy(text) {
	case Deny, Warn, Allow:
		return SharedCheckoutPolicy(text), nil
	default:
		return "", invalidPolicyError()
	}
}

func invalidPolicyError() error {
	return errors.New(`.rukrc.json sharedCheckoutPolicy must be "deny", "warn", or "allow"`)
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func joinKeys(keys []string) string {
	result := ""
	for index, key := range keys {
		if index > 0 {
			result += ", "
		}
		result += key
	}
	return result
}
