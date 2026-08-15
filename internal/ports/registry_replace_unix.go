//go:build !windows

package ports

import "os"

// POSIX rename atomically replaces a regular destination in the same
// directory. Temporary registry files are deliberately created beside the
// destination so this remains one filesystem operation.
func replaceRegistryFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
