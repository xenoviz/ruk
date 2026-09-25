//go:build windows

package lock

import (
	"errors"
	"os"
	"syscall"
)

// sharingViolation reports the Windows open failures caused by another
// process briefly holding a lock file open, such as a contender reading
// owner.json while it inspects or collects a lock directory.
func sharingViolation(err error) bool {
	return errors.Is(err, syscall.Errno(32)) || // ERROR_SHARING_VIOLATION
		errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}

// renameBusy reports a directory rename refused because a contender holds a
// file inside the directory open, which Windows reports as access denied.
func renameBusy(err error) bool {
	return sharingViolation(err) || errors.Is(err, os.ErrPermission)
}
