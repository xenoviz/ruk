//go:build windows

package atomicfile

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestRetryReplaceRetriesTransientSharingFailures(t *testing.T) {
	attempts := 0
	waits := make([]time.Duration, 0, 2)
	err := retryReplace(func() error {
		attempts++
		if attempts < 3 {
			return syscall.ERROR_ACCESS_DENIED
		}
		return nil
	}, func(delay time.Duration) {
		waits = append(waits, delay)
	})
	if err != nil {
		t.Fatalf("retryReplace returned an error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(waits) != 2 || waits[0] != 5*time.Millisecond || waits[1] != 10*time.Millisecond {
		t.Fatalf("waits = %v, want [5ms 10ms]", waits)
	}
}

func TestRetryReplaceStopsOnPermanentFailure(t *testing.T) {
	want := errors.New("permanent")
	attempts := 0
	err := retryReplace(func() error {
		attempts++
		return want
	}, func(time.Duration) {
		t.Fatal("pause called for a permanent error")
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}
