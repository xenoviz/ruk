//go:build windows

package atomicfile

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
	replaceAttempts         = 8
	replaceMaxDelay         = 80 * time.Millisecond
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	moveFileExProc = kernel32.NewProc("MoveFileExW")
)

// replaceFile uses the Windows replacement primitive instead of removing
// the destination first. Antivirus and indexers can briefly hold the
// file after a read, so retry only the documented sharing/lock failures while
// preserving the existing valid file throughout the bounded wait.
func replaceFile(oldPath, newPath string) error {
	oldUTF16, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return fmt.Errorf("encode temporary path: %w", err)
	}
	newUTF16, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return fmt.Errorf("encode destination path: %w", err)
	}
	return retryReplace(func() error {
		result, _, callErr := moveFileExProc.Call(
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

func retryReplace(attempt func() error, pause func(time.Duration)) error {
	delay := 5 * time.Millisecond
	var err error
	for index := 0; index < replaceAttempts; index++ {
		err = attempt()
		if err == nil {
			return nil
		}
		if !retryableReplace(err) || index == replaceAttempts-1 {
			return err
		}
		pause(delay)
		delay = min(delay*2, replaceMaxDelay)
	}
	return err
}

func retryableReplace(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, syscall.Errno(32)) || // ERROR_SHARING_VIOLATION
		errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}

// syncDirectory is a no-op: MoveFileEx with MOVEFILE_WRITE_THROUGH does not
// return until the rename has been flushed.
func syncDirectory(string) {}
