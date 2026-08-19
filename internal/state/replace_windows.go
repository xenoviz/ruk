//go:build windows

package state

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
	stateReplaceAttempts    = 8
	stateReplaceMaxDelay    = 80 * time.Millisecond
)

var (
	stateKernel32       = syscall.NewLazyDLL("kernel32.dll")
	stateMoveFileExProc = stateKernel32.NewProc("MoveFileExW")
)

// replaceStateFile uses the Windows replacement primitive instead of removing
// the destination first. Antivirus and indexers can briefly hold the state
// file after a read, so retry only the documented sharing/lock failures while
// preserving the existing valid state throughout the bounded wait.
func replaceStateFile(oldPath, newPath string) error {
	oldUTF16, err := syscall.UTF16PtrFromString(oldPath)
	if err != nil {
		return fmt.Errorf("encode state temporary path: %w", err)
	}
	newUTF16, err := syscall.UTF16PtrFromString(newPath)
	if err != nil {
		return fmt.Errorf("encode state destination path: %w", err)
	}
	return retryStateReplace(func() error {
		result, _, callErr := stateMoveFileExProc.Call(
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

func retryStateReplace(attempt func() error, pause func(time.Duration)) error {
	delay := 5 * time.Millisecond
	var err error
	for index := 0; index < stateReplaceAttempts; index++ {
		err = attempt()
		if err == nil {
			return nil
		}
		if !retryableStateReplace(err) || index == stateReplaceAttempts-1 {
			return err
		}
		pause(delay)
		delay = min(delay*2, stateReplaceMaxDelay)
	}
	return err
}

func retryableStateReplace(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, syscall.Errno(32)) || // ERROR_SHARING_VIOLATION
		errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}
