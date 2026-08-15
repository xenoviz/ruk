//go:build !windows

package process_test

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"

	processpkg "github.com/xenoviz/ruk/internal/process"
)

func TestNativeGroupSignalerRejectsSupervisorsOwnGroup(t *testing.T) {
	t.Parallel()

	group, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid returned an error: %v", err)
	}
	err = (processpkg.NativeGroupSignaler{}).SignalGroup(context.Background(), group, processpkg.SignalGraceful)
	if err == nil || !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("SignalGroup error = %v, want supervisor-group rejection", err)
	}
}

func TestNativeGroupSignalerRejectsInvalidGroups(t *testing.T) {
	t.Parallel()

	err := (processpkg.NativeGroupSignaler{}).SignalGroup(context.Background(), 0, processpkg.SignalForce)
	if err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("SignalGroup error = %v, want positive-group rejection", err)
	}
}
