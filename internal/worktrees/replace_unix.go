//go:build !windows

package worktrees

import "os"

// POSIX rename atomically replaces a file in the same directory.
func replaceIndexFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
