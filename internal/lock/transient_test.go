package lock

import (
	"errors"
	"testing"
)

func TestRetryTransientRetriesUntilSuccess(t *testing.T) {
	busy := errors.New("busy")
	calls := 0
	err := retryTransient(func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	}, func(err error) bool { return errors.Is(err, busy) })
	if err != nil || calls != 3 {
		t.Fatalf("retryTransient() = %v after %d calls, want nil after 3", err, calls)
	}
}

func TestRetryTransientStopsOnPermanentError(t *testing.T) {
	permanent := errors.New("permanent")
	calls := 0
	err := retryTransient(func() error {
		calls++
		return permanent
	}, func(error) bool { return false })
	if !errors.Is(err, permanent) || calls != 1 {
		t.Fatalf("retryTransient() = %v after %d calls, want permanent after 1", err, calls)
	}
}

func TestRetryTransientIsBounded(t *testing.T) {
	busy := errors.New("busy")
	calls := 0
	err := retryTransient(func() error {
		calls++
		return busy
	}, func(error) bool { return true })
	if !errors.Is(err, busy) || calls != transientAttempts {
		t.Fatalf("retryTransient() = %v after %d calls, want busy after %d", err, calls, transientAttempts)
	}
}
