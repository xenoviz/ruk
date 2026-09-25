//go:build !windows

package atomicfile

import "os"

// POSIX rename atomically replaces a file in the same directory.
func replaceFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

// syncDirectory persists the rename's directory entry.
func syncDirectory(dir string) {
	directory, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = directory.Sync()
	_ = directory.Close()
}
