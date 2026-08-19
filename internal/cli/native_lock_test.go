package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
)

func TestNativeDirectoryLockerPersistsCurrentNativeIdentity(t *testing.T) {
	locker, err := newNativeDirectoryLocker(context.Background())
	if err != nil {
		t.Fatalf("newNativeDirectoryLocker returned an error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "state.lock")
	guard, err := locker.Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("Acquire returned an error: %v", err)
	}
	defer guard.Release()

	data, err := os.ReadFile(filepath.Join(path, "owner.json"))
	if err != nil {
		t.Fatalf("read owner: %v", err)
	}
	var owner lock.Owner
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	identity, err := processpkg.CurrentIdentity(context.Background())
	if err != nil {
		t.Fatalf("current identity: %v", err)
	}
	if owner.PID != os.Getpid() || owner.Hostname != hostname || owner.ProcessIdentity != identity {
		t.Fatalf("owner = %#v, want pid=%d hostname=%q identity=%q", owner, os.Getpid(), hostname, identity)
	}
}
