//go:build windows

package worktrees

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
	indexReplaceAttempts    = 8
	indexReplaceMaxDelay    = 80 * time.Millisecond
)

var (
	indexKernel32       = syscall.NewLazyDLL("kernel32.dll")
	indexMoveFileExProc = indexKernel32.NewProc("MoveFileExW")
)

// replaceIndexFile uses the Windows replacement primitive instead of removing
// the destination first. Antivirus and indexers can briefly hold the index
// file after a read, so retry only the documented sharing/lock failures while
// preserving the existing valid index throughout the bounded wait.
func replaceIndexFile(oldPath, newPath string) error {
	oldUTF16, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return fmt.Errorf("encode index temporary path: %w", err)
	}
	newUTF16, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return fmt.Errorf("encode index destination path: %w", err)
	}
	return retryIndexReplace(func() error {
		result, _, callErr := indexMoveFileExProc.Call(
			uintptr(unsafe.Pointer(oldUTF16)),
			uintptr(unsafe.Pointer(newUTF16)),
			moveFileReplaceExisting|moveFileWriteThrough,
		)
		if result == 0 {
			return callErr
		}
		return nil
	}, time.Sleep)
}

func retryIndexReplace(attempt func() error, pause func(time.Duration)) error {
	delay := 5 * time.Millisecond
	var err error
	for index := 0; index < indexReplaceAttempts; index++ {
		err = attempt()
		if err == nil {
			return nil
		}
		if !retryableIndexReplace(err) || index == indexReplaceAttempts-1 {
			return err
		}
		pause(delay)
		delay = min(delay*2, indexReplaceMaxDelay)
	}
	return err
}

func retryableIndexReplace(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, syscall.Errno(32)) || // ERROR_SHARING_VIOLATION
		errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}
