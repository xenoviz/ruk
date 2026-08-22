package update

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

// OSFileSystem is the production filesystem implementation.
type OSFileSystem struct{}

func (OSFileSystem) Stat(name string) (fs.FileInfo, error)     { return os.Stat(name) }
func (OSFileSystem) Chmod(name string, mode fs.FileMode) error { return os.Chmod(name, mode) }
func (OSFileSystem) Copy(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	// Backups are security-sensitive update artifacts. Never follow or truncate
	// a pre-created path in a directory writable by another process.
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func (OSFileSystem) Rename(oldName, newName string) error { return os.Rename(oldName, newName) }
func (OSFileSystem) WriteFile(name string, bytes []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	_, writeErr := file.Write(bytes)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
func (OSFileSystem) Remove(name string) error {
	err := os.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
