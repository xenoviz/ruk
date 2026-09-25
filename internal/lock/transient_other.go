//go:build !windows

package lock

// sharingViolation is Windows-only: POSIX opens never fail because another
// process holds the file open.
func sharingViolation(error) bool { return false }

// renameBusy is Windows-only: POSIX renames ignore open files, so a permission
// failure is permanent.
func renameBusy(error) bool { return false }
