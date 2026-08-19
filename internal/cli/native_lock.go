package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
)

// newNativeDirectoryLocker constructs the production lock boundary with the
// same native process identity that lock recovery uses. Identity discovery is
// required: a locker that cannot prove its own owner must fail before any
// state or workspace mutation is attempted.
func newNativeDirectoryLocker(ctx context.Context) (*lock.DirectoryLocker, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		if err == nil {
			err = errors.New("hostname is empty")
		}
		return nil, fmt.Errorf("resolve lock hostname: %w", err)
	}
	identity, err := processpkg.CurrentIdentity(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve lock process identity: %w", err)
	}
	return lock.NewDirectoryLocker(lock.Config{
		PID:             os.Getpid(),
		Hostname:        hostname,
		ProcessIdentity: identity,
		Probe:           processpkg.Inspector{},
	}), nil
}
