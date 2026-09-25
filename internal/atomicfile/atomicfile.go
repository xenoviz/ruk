// Package atomicfile durably replaces small owner-only metadata files.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Replace writes data to a same-directory temporary file, flushes it to
// stable storage, and atomically renames it over path, so a crash leaves
// either the previous or the new complete contents, never a truncated file.
// label names the file in error messages, for example "state". Callers must
// serialize writers to path; the temporary name is derived from the process.
func Replace(path string, data []byte, label string) (result error) {
	temporary := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	committed := false
	defer func() {
		if !committed {
			if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) && result == nil {
				result = fmt.Errorf("remove temporary %s %s: %w", label, temporary, err)
			}
		}
	}()

	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write temporary %s %s: %w", label, temporary, err)
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fmt.Errorf("write temporary %s %s: %w", label, temporary, writeErr)
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return fmt.Errorf("secure temporary %s %s: %w", label, temporary, err)
	}
	if err := replaceFile(temporary, path); err != nil {
		return fmt.Errorf("replace %s %s: %w", label, path, err)
	}
	committed = true
	// The rename is already visible; directory durability is best effort so
	// a filesystem without directory fsync cannot turn a commit into a
	// reported failure.
	syncDirectory(filepath.Dir(path))
	return nil
}
