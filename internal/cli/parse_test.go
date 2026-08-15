package cli_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/xenoviz/ruk/internal/cli"
)

func TestParseInvocationCoversPublicCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want cli.Invocation
	}{
		{name: "init", args: []string{"init", "--json"}, want: cli.Invocation{Name: "init", JSON: true}},
		{name: "create", args: []string{"create", "agent/api", "--path", "slot", "--from", "upstream/main", "--fetch", "--detach", "--json"}, want: cli.Invocation{Name: "create", Branch: "agent/api", Path: "slot", From: "upstream/main", Fetch: true, Detach: true, JSON: true}},
		{name: "acquire", args: []string{"acquire", "agent/api", "--ttl", "90", "--owner", "codex", "--port", "api", "--port", "ui", "--json"}, want: cli.Invocation{Name: "acquire", Branch: "agent/api", TTL: "90", Owner: "codex", Ports: []string{"api", "ui"}, JSON: true}},
		{name: "last scalar wins", args: []string{"acquire", "agent/api", "--owner", "one", "--owner", "two"}, want: cli.Invocation{Name: "acquire", Branch: "agent/api", Owner: "two"}},
		{name: "renew", args: []string{"renew", "assignment-1", "--ttl", "45", "--json"}, want: cli.Invocation{Name: "renew", AssignmentID: "assignment-1", TTL: "45", JSON: true}},
		{name: "release", args: []string{"release", "assignment-1", "--force", "--json"}, want: cli.Invocation{Name: "release", AssignmentID: "assignment-1", Force: true, JSON: true}},
		{name: "sync", args: []string{"sync", "--allow-shared-checkout", "--json"}, want: cli.Invocation{Name: "sync", AllowSharedCheckout: true, JSON: true}},
		{name: "run", args: []string{"run", "--allow-shared-checkout", "--", "tool", "--flag"}, want: cli.Invocation{Name: "run", AllowSharedCheckout: true, Command: []string{"tool", "--flag"}}},
		{name: "run without delimiter", args: []string{"run", "tool", "--flag"}, want: cli.Invocation{Name: "run", Command: []string{"tool", "--flag"}}},
		{name: "exec", args: []string{"exec", "agent/api", "--from", "main", "--fetch", "--ttl", "30", "--owner", "codex", "--port", "api", "--", "tool", "--flag"}, want: cli.Invocation{Name: "exec", Branch: "agent/api", From: "main", Fetch: true, TTL: "30", Owner: "codex", Ports: []string{"api"}, Command: []string{"tool", "--flag"}}},
		{name: "exec without delimiter", args: []string{"exec", "agent/api", "--ttl", "30", "tool", "--flag"}, want: cli.Invocation{Name: "exec", Branch: "agent/api", TTL: "30", Command: []string{"tool", "--flag"}}},
		{name: "warm", args: []string{"warm", "--count", "3", "--from", "main", "--fetch", "--json"}, want: cli.Invocation{Name: "warm", Count: "3", From: "main", Fetch: true, JSON: true}},
		{name: "shell", args: []string{"shell", "agent/api", "--ttl", "30", "--owner", "codex", "--port", "api"}, want: cli.Invocation{Name: "shell", Branch: "agent/api", TTL: "30", Owner: "codex", Ports: []string{"api"}}},
		{name: "list", args: []string{"list", "--json"}, want: cli.Invocation{Name: "list", JSON: true}},
		{name: "remove", args: []string{"remove", "slot", "--force"}, want: cli.Invocation{Name: "remove", Path: "slot", Force: true}},
		{name: "status", args: []string{"status", "--explain", "--json"}, want: cli.Invocation{Name: "status", Explain: true, JSON: true}},
		{name: "stats", args: []string{"stats", "--disk", "--json"}, want: cli.Invocation{Name: "stats", Disk: true, JSON: true}},
		{name: "gc", args: []string{"gc", "--max-age", "120", "--apply", "--force-expired", "--json"}, want: cli.Invocation{Name: "gc", MaxAge: "120", Apply: true, ForceExpired: true, JSON: true}},
		{name: "update", args: []string{"update", "--check", "--json"}, want: cli.Invocation{Name: "update", Check: true, JSON: true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := cli.Parse(test.args)
			if err != nil {
				t.Fatalf("Parse returned an error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("invocation = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseInvocationRejectsInvalidContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown command", args: []string{"deploy"}, want: "Unknown command deploy"},
		{name: "unknown option", args: []string{"list", "--force"}, want: "Unknown option --force"},
		{name: "missing option value", args: []string{"acquire", "agent/api", "--ttl"}, want: "--ttl requires a value"},
		{name: "option used as value", args: []string{"acquire", "agent/api", "--ttl", "--json"}, want: "--ttl requires a value"},
		{name: "missing branch", args: []string{"acquire"}, want: "acquire requires exactly one branch name"},
		{name: "extra positional", args: []string{"list", "extra"}, want: "list does not accept positional arguments"},
		{name: "run missing command", args: []string{"run", "--"}, want: "run requires a command"},
		{name: "exec missing command", args: []string{"exec", "agent/api", "--"}, want: "exec requires a command"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := cli.Parse(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}
