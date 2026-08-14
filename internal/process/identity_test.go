package process

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestInspectorRejectsInvalidProcessIdentifiers(t *testing.T) {
	t.Parallel()

	state, err := (Inspector{}).Inspect(context.Background(), 0)
	if err != nil {
		t.Fatalf("Inspect returned an error: %v", err)
	}
	if state.Alive || state.IdentityKnown || state.Identity != "" {
		t.Fatalf("invalid PID state = %#v", state)
	}
}

func TestInspectorReturnsStableCurrentProcessIdentity(t *testing.T) {
	t.Parallel()

	inspector := Inspector{}
	first, err := inspector.Inspect(context.Background(), os.Getpid())
	if err != nil {
		t.Fatalf("first Inspect returned an error: %v", err)
	}
	second, err := inspector.Inspect(context.Background(), os.Getpid())
	if err != nil {
		t.Fatalf("second Inspect returned an error: %v", err)
	}
	if !first.Alive || !first.IdentityKnown || first.Identity == "" {
		t.Fatalf("first state = %#v", first)
	}
	if second != first {
		t.Fatalf("identity changed: first %#v, second %#v", first, second)
	}
}

func TestCurrentIdentityUsesNativeInspector(t *testing.T) {
	t.Parallel()

	identity, err := CurrentIdentity(context.Background())
	if err != nil {
		t.Fatalf("CurrentIdentity returned an error: %v", err)
	}
	if identity == "" {
		t.Fatal("CurrentIdentity returned an empty identity")
	}
}

func TestWindowsFiletimeMatchesDotNetTicks(t *testing.T) {
	t.Parallel()

	const unixEpochFiletime = uint64(116_444_736_000_000_000)
	if got := dotNetTicks(unixEpochFiletime); got != 621_355_968_000_000_000 {
		t.Fatalf("dotNetTicks = %d", got)
	}
	if got := strconv.FormatUint(dotNetTicks(unixEpochFiletime), 10); got != "621355968000000000" {
		t.Fatalf("formatted ticks = %q", got)
	}
}

func TestPOSIXIdentityMatchesPSStartTimeShape(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, time.August, 15, 6, 7, 8, 0, time.Local)
	if got := formatPOSIXIdentity(started); got != "Sat Aug 15 06:07:08 2026" {
		t.Fatalf("identity = %q", got)
	}
}
