package process_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/xenoviz/ruk/internal/lock"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

func TestNativeTrackerRecognizesCurrentProcess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	observed, err := (processpkg.Inspector{}).Inspect(ctx, os.Getpid())
	if err != nil {
		t.Fatalf("Inspect returned an error: %v", err)
	}
	if !observed.Alive || !observed.IdentityKnown {
		t.Fatalf("current process state = %#v", observed)
	}
	exists, err := processpkg.NewTracker().Exists(ctx, state.TrackedProcessRecord{
		PID:       int64(os.Getpid()),
		StartedAt: observed.Identity,
	})
	if err != nil {
		t.Fatalf("Exists returned an error: %v", err)
	}
	if !exists {
		t.Fatal("current process was reported absent")
	}
}

type identityProbe func(context.Context, int) (lock.ProcessState, error)

func (probe identityProbe) Inspect(ctx context.Context, pid int) (lock.ProcessState, error) {
	return probe(ctx, pid)
}

func TestTrackedProcessExistsRequiresMatchingIdentity(t *testing.T) {
	t.Parallel()

	tracker := processpkg.Tracker{
		Probe: identityProbe(func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{Alive: true, IdentityKnown: true, Identity: "original"}, nil
		}),
		DescendantsExist: func(context.Context, int) (bool, error) { return false, nil },
	}
	exists, err := tracker.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "original"})
	if err != nil {
		t.Fatalf("Exists returned an error: %v", err)
	}
	if !exists {
		t.Fatal("matching live process was reported absent")
	}
}

func TestTrackedProcessExistsRejectsReusedLeaderWithDescendants(t *testing.T) {
	t.Parallel()

	tracker := processpkg.Tracker{
		Probe: identityProbe(func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{Alive: true, IdentityKnown: true, Identity: "replacement"}, nil
		}),
		DescendantsExist: func(context.Context, int) (bool, error) { return true, nil },
	}
	_, err := tracker.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "original"})
	var unavailable *processpkg.IdentityUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Exists error = %v, want IdentityUnavailableError", err)
	}
}

func TestTrackedProcessExistsClearsReusedLeaderWithoutDescendants(t *testing.T) {
	t.Parallel()

	tracker := processpkg.Tracker{
		Probe: identityProbe(func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{Alive: true, IdentityKnown: true, Identity: "replacement"}, nil
		}),
		DescendantsExist: func(context.Context, int) (bool, error) { return false, nil },
	}
	exists, err := tracker.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "original"})
	if err != nil {
		t.Fatalf("Exists returned an error: %v", err)
	}
	if exists {
		t.Fatal("reused leader without descendants was retained")
	}
}

func TestTrackedProcessExistsFailsClosedWhenInspectionIsUnknown(t *testing.T) {
	t.Parallel()

	tests := map[string]identityProbe{
		"identity unknown": func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{Alive: true, IdentityKnown: false}, nil
		},
		"probe failed": func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{}, errors.New("inspection unavailable")
		},
	}
	for name, probe := range tests {
		probe := probe
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tracker := processpkg.Tracker{
				Probe:            probe,
				DescendantsExist: func(context.Context, int) (bool, error) { return false, nil },
			}
			_, err := tracker.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "original"})
			var unavailable *processpkg.IdentityUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("Exists error = %v, want IdentityUnavailableError", err)
			}
		})
	}
}

func TestTrackedProcessExistsRetainsLeaderlessTree(t *testing.T) {
	t.Parallel()

	tracker := processpkg.Tracker{
		Probe: identityProbe(func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{}, nil
		}),
		DescendantsExist: func(context.Context, int) (bool, error) { return true, nil },
	}
	_, err := tracker.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "original"})
	var unavailable *processpkg.IdentityUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Exists error = %v, want IdentityUnavailableError", err)
	}
}

func TestTrackedProcessExistsReportsFinishedTree(t *testing.T) {
	t.Parallel()

	tracker := processpkg.Tracker{
		Probe: identityProbe(func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{}, nil
		}),
		DescendantsExist: func(context.Context, int) (bool, error) { return false, nil },
	}
	exists, err := tracker.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "original"})
	if err != nil {
		t.Fatalf("Exists returned an error: %v", err)
	}
	if exists {
		t.Fatal("finished tree was reported active")
	}
}

func TestTrackedProcessExistsFailsClosedWhenDescendantEnumerationFails(t *testing.T) {
	t.Parallel()

	tracker := processpkg.Tracker{
		Probe: identityProbe(func(context.Context, int) (lock.ProcessState, error) {
			return lock.ProcessState{}, nil
		}),
		DescendantsExist: func(context.Context, int) (bool, error) {
			return false, errors.New("enumeration unavailable")
		},
	}
	_, err := tracker.Exists(context.Background(), state.TrackedProcessRecord{PID: 42, StartedAt: "original"})
	var unavailable *processpkg.IdentityUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("Exists error = %v, want IdentityUnavailableError", err)
	}
}
