package cli

import (
	"errors"
	"fmt"
	"strings"
)

// Invocation is the validated syntactic form of one public Ruk command.
// Numeric values remain strings here so command handlers can preserve the
// legacy validation messages and avoid parsing an option before it is used.
type Invocation struct {
	Name                string
	Branch              string
	AssignmentID        string
	Path                string
	From                string
	TTL                 string
	Owner               string
	MaxAge              string
	Count               string
	Ports               []string
	Command             []string
	Fetch               bool
	Detach              bool
	JSON                bool
	Force               bool
	Check               bool
	Apply               bool
	ForceExpired        bool
	Explain             bool
	Disk                bool
	AllowSharedCheckout bool
	All                 bool
}

type optionSpec struct {
	values map[string]bool
	flags  map[string]bool
}

// Parse validates the command/option grammar while preserving the TypeScript
// CLI's public behavior, including repeated --port values, last-scalar-wins,
// and delimiter-less run/exec command forms.
func Parse(args []string) (Invocation, error) {
	if len(args) == 0 {
		return Invocation{}, errors.New("command is required")
	}
	name := args[0]
	rest := args[1:]
	if name == "run" {
		return parseRun(rest)
	}
	if name == "exec" {
		return parseExec(rest)
	}

	spec, positions, usage, ok := commandGrammar(name)
	if !ok {
		return Invocation{}, fmt.Errorf("Unknown command %s. Run ruk --help.", name)
	}
	invocation := Invocation{Name: name}
	positional, err := parseOptions(rest, spec, &invocation)
	if err != nil {
		return Invocation{}, err
	}
	if len(positional) != positions {
		return Invocation{}, errors.New(usage)
	}
	assignPositionals(&invocation, positional)
	return invocation, nil
}

func parseRun(args []string) (Invocation, error) {
	invocation := Invocation{Name: "run"}
	separator := indexOf(args, "--")
	var command []string
	if separator >= 0 {
		positional, err := parseOptions(args[:separator], spec(nil, []string{"--allow-shared-checkout"}), &invocation)
		if err != nil {
			return Invocation{}, err
		}
		if len(positional) != 0 {
			return Invocation{}, errors.New("run options must appear before --")
		}
		command = args[separator+1:]
	} else if len(args) > 0 && args[0] == "--allow-shared-checkout" {
		invocation.AllowSharedCheckout = true
		command = args[1:]
	} else {
		command = args
	}
	if len(command) == 0 {
		return Invocation{}, errors.New("run requires a command")
	}
	invocation.Command = append([]string(nil), command...)
	return invocation, nil
}

func parseExec(args []string) (Invocation, error) {
	invocation := Invocation{Name: "exec"}
	if len(args) > 0 && args[0] == "--" {
		if len(args) == 1 {
			return Invocation{}, errors.New("exec requires a command")
		}
		invocation.Command = append([]string(nil), args[1:]...)
		return invocation, nil
	}

	separator := indexOf(args, "--")
	var acquireArgs, command []string
	if separator >= 0 {
		acquireArgs = args[:separator]
		command = args[separator+1:]
	} else {
		acquireArgs, command = splitExec(args)
	}
	if len(acquireArgs) == 0 {
		return Invocation{}, errors.New("exec requires a branch")
	}
	positional, err := parseOptions(acquireArgs, spec([]string{"--from", "--ttl", "--owner", "--port"}, []string{"--fetch"}), &invocation)
	if err != nil {
		return Invocation{}, err
	}
	if len(positional) != 1 {
		return Invocation{}, errors.New("acquire requires exactly one branch name")
	}
	invocation.Branch = positional[0]
	if len(command) == 0 {
		return Invocation{}, errors.New("exec requires a command")
	}
	invocation.Command = append([]string(nil), command...)
	return invocation, nil
}

func splitExec(args []string) ([]string, []string) {
	if len(args) == 0 {
		return nil, nil
	}
	acquire := []string{args[0]}
	for index := 1; index < len(args); {
		argument := args[index]
		if argument == "--fetch" {
			acquire = append(acquire, argument)
			index++
			continue
		}
		if isOneOf(argument, "--from", "--ttl", "--owner", "--port") {
			if index+1 >= len(args) {
				return append(acquire, argument), nil
			}
			acquire = append(acquire, argument, args[index+1])
			index += 2
			continue
		}
		return acquire, args[index:]
	}
	return acquire, nil
}

func commandGrammar(name string) (optionSpec, int, string, bool) {
	switch name {
	case "init":
		return spec(nil, []string{"--json"}), 0, "init does not accept positional arguments", true
	case "create":
		return spec([]string{"--path", "--from"}, []string{"--fetch", "--detach", "--json"}), 1, "create requires exactly one branch name", true
	case "acquire":
		return spec([]string{"--from", "--ttl", "--owner", "--port"}, []string{"--fetch", "--json"}), 1, "acquire requires exactly one branch name", true
	case "renew":
		return spec([]string{"--ttl"}, []string{"--json"}), 1, "renew requires exactly one assignment ID", true
	case "release":
		return spec(nil, []string{"--force", "--json"}), 1, "release requires exactly one assignment ID", true
	case "sync":
		return spec(nil, []string{"--json", "--allow-shared-checkout"}), 0, "sync does not accept positional arguments", true
	case "warm":
		return spec([]string{"--count", "--from"}, []string{"--fetch", "--json"}), 0, "warm does not accept positional arguments", true
	case "shell":
		return spec([]string{"--from", "--ttl", "--owner", "--port"}, []string{"--fetch"}), 1, "shell requires exactly one branch name", true
	case "list":
		return spec(nil, []string{"--json"}), 0, "list does not accept positional arguments", true
	case "worktrees":
		return spec(nil, []string{"--json", "--all"}), 0, "worktrees does not accept positional arguments", true
	case "remove":
		return spec(nil, []string{"--force"}), 1, "remove requires exactly one workspace path", true
	case "status":
		return spec(nil, []string{"--explain", "--json"}), 0, "status does not accept positional arguments", true
	case "stats":
		return spec(nil, []string{"--disk", "--json"}), 0, "stats does not accept positional arguments", true
	case "gc":
		return spec([]string{"--max-age"}, []string{"--apply", "--force-expired", "--json"}), 0, "gc does not accept positional arguments", true
	case "update":
		return spec(nil, []string{"--check", "--json"}), 0, "update does not accept positional arguments", true
	default:
		return optionSpec{}, 0, "", false
	}
}

func spec(values, flags []string) optionSpec {
	result := optionSpec{values: make(map[string]bool, len(values)), flags: make(map[string]bool, len(flags))}
	for _, value := range values {
		result.values[value] = true
	}
	for _, flag := range flags {
		result.flags[flag] = true
	}
	return result
}

func parseOptions(args []string, grammar optionSpec, invocation *Invocation) ([]string, error) {
	positional := make([]string, 0)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !strings.HasPrefix(argument, "--") {
			positional = append(positional, argument)
			continue
		}
		if grammar.flags[argument] {
			setFlag(invocation, argument)
			continue
		}
		if grammar.values[argument] {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				return nil, fmt.Errorf("%s requires a value", argument)
			}
			setValue(invocation, argument, args[index+1])
			index++
			continue
		}
		return nil, fmt.Errorf("Unknown option %s", argument)
	}
	return positional, nil
}

func setFlag(invocation *Invocation, option string) {
	switch option {
	case "--fetch":
		invocation.Fetch = true
	case "--detach":
		invocation.Detach = true
	case "--json":
		invocation.JSON = true
	case "--force":
		invocation.Force = true
	case "--check":
		invocation.Check = true
	case "--apply":
		invocation.Apply = true
	case "--force-expired":
		invocation.ForceExpired = true
	case "--explain":
		invocation.Explain = true
	case "--disk":
		invocation.Disk = true
	case "--allow-shared-checkout":
		invocation.AllowSharedCheckout = true
	case "--all":
		invocation.All = true
	}
}

func setValue(invocation *Invocation, option, value string) {
	switch option {
	case "--path":
		invocation.Path = value
	case "--from":
		invocation.From = value
	case "--ttl":
		invocation.TTL = value
	case "--owner":
		invocation.Owner = value
	case "--max-age":
		invocation.MaxAge = value
	case "--count":
		invocation.Count = value
	case "--port":
		invocation.Ports = append(invocation.Ports, value)
	}
}

func assignPositionals(invocation *Invocation, positional []string) {
	if len(positional) == 0 {
		return
	}
	switch invocation.Name {
	case "create", "acquire", "shell":
		invocation.Branch = positional[0]
	case "renew", "release":
		invocation.AssignmentID = positional[0]
	case "remove":
		invocation.Path = positional[0]
	}
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func isOneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
