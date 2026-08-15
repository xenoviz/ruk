//go:build windows

package ports

import (
	"fmt"
	"syscall"
	"unsafe"
)

const moveFileReplaceExisting = 0x1

var (
	registryKernel32       = syscall.NewLazyDLL("kernel32.dll")
	registryMoveFileExProc = registryKernel32.NewProc("MoveFileExW")
)

// replaceRegistryFile uses MoveFileExW(REPLACE_EXISTING), because
// syscall.Rename/os.Rename call MoveFileW on Windows and fail when ports.json
// already exists. The source and destination are in one directory, so the
// replacement is atomic at the filesystem boundary; the destination is never
// removed first.
func replaceRegistryFile(oldPath, newPath string) error {
	oldUTF16, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return fmt.Errorf("encode registry temporary path: %w", err)
	}
	newUTF16, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return fmt.Errorf("encode registry destination path: %w", err)
	}
	result, _, callErr := registryMoveFileExProc.Call(
		uintptr(unsafe.Pointer(oldUTF16)),
		uintptr(unsafe.Pointer(newUTF16)),
		moveFileReplaceExisting,
	)
	if result == 0 {
		return fmt.Errorf("replace registry file: %w", callErr)
	}
	return nil
}
