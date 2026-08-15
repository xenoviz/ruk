package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/cli"
	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

const activityKeeperID = "11111111-1111-4111-8111-111111111111"

type activityStore struct {
	mu      sync.Mutex
	current *state.State
	fail    error
}

func (store *activityStore) Read(context.Context) (*state.State, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	encoded, err := json.Marshal(store.current)
	if err != nil {
		return nil, err
	}
	return state.Decode(encoded, "activity test state")
}

func (store *activityStore) Update(ctx context.Context, mutate func(*state.State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.fail != nil {
		return store.fail
	}
	return mutate(store.current)
}

type activityTicker struct {
	ticks   chan time.Time
	stopped chan struct{}
}

type activityClock struct{ nanos atomic.Int64 }

func newActivityClock(value time.Time) *activityClock {
	clock := &activityClock{}
	clock.nanos.Store(value.UnixNano())
	return clock
}

func (clock *activityClock) Now() time.Time {
	return time.Unix(0, clock.nanos.Load()).UTC()
}

func (clock *activityClock) Add(duration time.Duration) time.Time {
	return time.Unix(0, clock.nanos.Add(int64(duration))).UTC()
}

func (ticker *activityTicker) C() <-chan time.Time { return ticker.ticks }
func (ticker *activityTicker) Stop() {
	select {
	case <-ticker.stopped:
	default:
		close(ticker.stopped)
	}
}

func activityFixture(t *testing.T, leaseMinutes float64) (*activityStore, *lifecycle.Service, string, string, *activityClock) {
	t.Helper()
	path := t.TempDir()
	key, err := state.TreeKey(path)
	if err != nil {
		t.Fatalf("TreeKey returned an error: %v", err)
	}
	store := &activityStore{current: &state.State{Version: state.CurrentVersion, Trees: map[string]state.TreeRecord{}, Workspaces: map[string]state.WorkspaceRecord{}, Metrics: state.EmptyMetrics()}}
	store.current.Workspaces[key] = state.WorkspaceRecord{
		Path: path, Managed: true, Lifecycle: state.LifecycleAssigned,
		Assignment: &state.AssignmentRecord{ID: "assignment-1", Owner: "owner", Hostname: "host", AssignedAt: "2026-01-01T00:00:00.000Z", RenewedAt: "2026-01-01T00:00:00.000Z", ExpiresAt: "2026-01-01T01:00:00.000Z", LeaseDurationMinutes: leaseMinutes, LastActivityAt: "2026-01-01T00:00:00.000Z", LeaseKeepers: []state.LeaseKeeperRecord{}, Ports: map[string]int64{}},
		Processes:  []state.TrackedProcessRecord{}, CreatedAt: "2026-01-01T00:00:00.000Z", UpdatedAt: "2026-01-01T00:00:00.000Z",
	}
	clock := newActivityClock(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	service := lifecycle.New(store, lifecycle.Options{Now: clock.Now, NewID: func() string { return "unused" }})
	return store, service, path, key, clock
}

func TestActivityRunnerRefreshesAndCleansKeeper(t *testing.T) {
	store, lifecycleService, _, key, now := activityFixture(t, 60)
	ticker := &activityTicker{ticks: make(chan time.Time, 2), stopped: make(chan struct{})}
	runner := cli.NewActivityRunner(cli.ActivityRunnerOptions{
		Lifecycle: lifecycleService, Reader: store, Now: now.Now, NewID: func() string { return activityKeeperID },
		Ticker: func(time.Duration) cli.ActivityTicker { return ticker },
	})
	operationStarted := make(chan struct{})
	operationDone := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(context.Background(), "assignment-1", func(context.Context) error {
			close(operationStarted)
			<-operationDone
			return nil
		})
	}()
	select {
	case <-operationStarted:
	case err := <-result:
		t.Fatalf("activity runner returned before starting the operation: %v", err)
	case <-time.After(time.Second):
		t.Fatal("activity operation did not start")
	}
	ticker.ticks <- now.Add(time.Minute)
	deadline := time.After(time.Second)
	for {
		store.mu.Lock()
		refreshed := len(store.current.Workspaces[key].Assignment.LeaseKeepers) == 1 && store.current.Workspaces[key].Assignment.LeaseKeepers[0].HeartbeatAt == "2026-01-01T00:01:00.000Z"
		store.mu.Unlock()
		if refreshed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("activity heartbeat did not refresh")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(operationDone)
	if err := <-result; err != nil {
		t.Fatalf("activity runner returned an error: %v", err)
	}
	store.mu.Lock()
	keepers := store.current.Workspaces[key].Assignment.LeaseKeepers
	store.mu.Unlock()
	if len(keepers) != 0 {
		t.Fatalf("keeper was not cleaned: %#v", keepers)
	}
}

func TestActivityRunnerRefreshPreservesConcurrentExplicitRenewal(t *testing.T) {
	store, lifecycleService, _, key, now := activityFixture(t, 60)
	ticker := &activityTicker{ticks: make(chan time.Time, 1), stopped: make(chan struct{})}
	runner := cli.NewActivityRunner(cli.ActivityRunnerOptions{Lifecycle: lifecycleService, Reader: store, Now: now.Now, NewID: func() string { return activityKeeperID }, Ticker: func(time.Duration) cli.ActivityTicker { return ticker }})
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(context.Background(), "assignment-1", func(context.Context) error { <-done; return nil })
	}()
	now.Add(time.Minute)
	renewed, err := lifecycleService.RenewAssignment(context.Background(), "assignment-1", now.Now().Add(10*time.Hour), nil)
	if err != nil {
		t.Fatalf("RenewAssignment returned an error: %v", err)
	}
	wantExpires := renewed.Assignment.ExpiresAt
	ticker.ticks <- now.Now()
	deadline := time.After(time.Second)
	for {
		store.mu.Lock()
		expires := store.current.Workspaces[key].Assignment.ExpiresAt
		store.mu.Unlock()
		if expires == wantExpires {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("explicit renewal was shortened; expiresAt=%s", expires)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(done)
	if err := <-result; err != nil {
		t.Fatalf("activity runner returned an error: %v", err)
	}
}

func TestActivityRunnerCancellationStopsOperationAndCleansKeeper(t *testing.T) {
	store, lifecycleService, _, key, _ := activityFixture(t, 60)
	ticker := &activityTicker{ticks: make(chan time.Time), stopped: make(chan struct{})}
	runner := cli.NewActivityRunner(cli.ActivityRunnerOptions{Lifecycle: lifecycleService, Reader: store, NewID: func() string { return activityKeeperID }, Ticker: func(time.Duration) cli.ActivityTicker { return ticker }})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx, "assignment-1", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	store.mu.Lock()
	keepers := store.current.Workspaces[key].Assignment.LeaseKeepers
	store.mu.Unlock()
	if len(keepers) != 0 {
		t.Fatalf("keeper survived cancellation: %#v", keepers)
	}
}

func TestActivityRunnerOperationFailureIsPreserved(t *testing.T) {
	store, lifecycleService, _, key, _ := activityFixture(t, 60)
	ticker := &activityTicker{ticks: make(chan time.Time), stopped: make(chan struct{})}
	runner := cli.NewActivityRunner(cli.ActivityRunnerOptions{Lifecycle: lifecycleService, Reader: store, NewID: func() string { return activityKeeperID }, Ticker: func(time.Duration) cli.ActivityTicker { return ticker }})
	operationErr := errors.New("command failed")
	if err := runner.Run(context.Background(), "assignment-1", func(context.Context) error { return operationErr }); !errors.Is(err, operationErr) {
		t.Fatalf("operation error = %v", err)
	}
	store.mu.Lock()
	keepers := store.current.Workspaces[key].Assignment.LeaseKeepers
	store.mu.Unlock()
	if len(keepers) != 0 {
		t.Fatalf("keeper survived operation failure: %#v", keepers)
	}
}

func TestActivityRunnerJoinsCleanupFailureAsAssignmentActivityError(t *testing.T) {
	store, lifecycleService, _, _, _ := activityFixture(t, 60)
	ticker := &activityTicker{ticks: make(chan time.Time), stopped: make(chan struct{})}
	cleanupErr := errors.New("state lock unavailable")
	runner := cli.NewActivityRunner(cli.ActivityRunnerOptions{Lifecycle: lifecycleService, Reader: store, NewID: func() string { return activityKeeperID }, Ticker: func(time.Duration) cli.ActivityTicker { return ticker }})
	err := runner.Run(context.Background(), "assignment-1", func(context.Context) error {
		store.mu.Lock()
		store.fail = cleanupErr
		store.mu.Unlock()
		return nil
	})
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("cleanup error = %v", err)
	}
	var activityErr *cli.AssignmentActivityError
	if !errors.As(err, &activityErr) {
		t.Fatalf("error = %T %v, want AssignmentActivityError", err, err)
	}
}
