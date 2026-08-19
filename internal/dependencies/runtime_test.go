package dependencies

import (
	"context"
	"testing"
)

func TestResolveRuntimeIdentityUsesStandardRuntimeProbe(t *testing.T) {
	var requests []CommandRequest
	identity, err := ResolveRuntimeIdentity(context.Background(), t.TempDir(), PackageManager{Name: "npm", Version: "11.0.0", Command: []string{"npm", "install"}}, func(_ context.Context, request CommandRequest) (CommandResult, error) {
		requests = append(requests, request)
		if request.Command == "node" && len(request.Args) == 1 && request.Args[0] == "--version" {
			return CommandResult{Stdout: "22.0.0\n"}, nil
		}
		return CommandResult{Stdout: "115\n"}, nil
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeIdentity returned an error: %v", err)
	}
	if identity.Runtime != "node" || identity.Version != "22.0.0" || identity.NativeABI != "115" || len(requests) != 2 || requests[0].Command != "node" {
		t.Fatalf("identity/requests = %#v/%#v", identity, requests)
	}
}

func TestResolveRuntimeIdentityDoesNotExecuteCustomCommand(t *testing.T) {
	called := false
	identity, err := ResolveRuntimeIdentity(context.Background(), t.TempDir(), PackageManager{Name: "custom", Version: UnknownManagerVersion}, func(context.Context, CommandRequest) (CommandResult, error) {
		called = true
		return CommandResult{}, nil
	})
	if err != nil || called || identity.Runtime != "unknown" || identity.Version != "unknown" || identity.NativeABI != "unknown" {
		t.Fatalf("identity/call/error = %#v/%v/%v", identity, called, err)
	}
}
