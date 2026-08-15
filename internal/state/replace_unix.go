//go:build !windows

package state

import "os"

// POSIX rename atomically replaces a file in the same directory.
func replaceStateFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
