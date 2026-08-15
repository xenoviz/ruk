package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/state"
)

const maxActivityHeartbeatInterval = 5 * time.Minute

// ActivityTicker is the small timer boundary used by the heartbeat loop.
type ActivityTicker interface {
	C() <-chan time.Time
	Stop()
}

// ActivityTickerFactory creates one bounded heartbeat timer. A factory rather
// than time.Ticker itself makes interval changes and deterministic tests
// straightforward.
type ActivityTickerFactory func(time.Duration) ActivityTicker

type nativeActivityTicker struct{ ticker *time.Ticker }

func (ticker nativeActivityTicker) C() <-chan time.Time { return ticker.ticker.C }
func (ticker nativeActivityTicker) Stop()               { ticker.ticker.Stop() }

// ActivityRunnerOptions configures the production ExecuteActivityRunner
// adapter. Reader supplies the current stored lease duration before each
// keeper and after each refresh; Lifecycle owns all ID-fenced mutations.
type ActivityRunnerOptions struct {
	Lifecycle *lifecycle.Service
	Reader    ExecuteStateReader
	Now       func() time.Time
	NewID     func() string
	Ticker    ActivityTickerFactory
}

// LifecycleActivityRunner is an ExecuteActivityRunner implementation.
type LifecycleActivityRunner struct {
	lifecycle *lifecycle.Service
	reader    ExecuteStateReader
	now       func() time.Time
	newID     func() string
	ticker    ActivityTickerFactory
}

// NewActivityRunner creates a production activity adapter.
func NewActivityRunner(options ActivityRunnerOptions) *LifecycleActivityRunner {
	if options.Lifecycle == nil {
		panic("cli: nil activity lifecycle")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomActivityUUID
	}
	ticker := options.Ticker
	if ticker == nil {
		ticker = func(interval time.Duration) ActivityTicker {
			return nativeActivityTicker{ticker: time.NewTicker(interval)}
		}
	}
	return &LifecycleActivityRunner{lifecycle: options.Lifecycle, reader: options.Reader, now: now, newID: newID, ticker: ticker}
}

// ExecuteActivityRunner returns the callback shape accepted by ExecuteService.
func (runner *LifecycleActivityRunner) ExecuteActivityRunner() ExecuteActivityRunner {
	return runner.Run
}

// Run surrounds one operation with an assignment-fenced lease keeper. The
// operation and heartbeat stop on completion/cancellation; keeper cleanup is
// always attempted with a context that is independent of caller cancellation.
func (runner *LifecycleActivityRunner) Run(ctx context.Context, assignmentID string, operation func(context.Context) error) error {
	if runner == nil || runner.lifecycle == nil {
		return errors.New("activity runner is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if assignmentID == "" {
		return errors.New("assignment ID must not be empty")
	}
	if operation == nil {
		return errors.New("activity operation must not be nil")
	}
	if runner.reader == nil {
		return errors.New("activity state reader is not configured")
	}
	if runner.ticker == nil || runner.newID == nil || runner.now == nil {
		return errors.New("activity runner is not fully configured")
	}
	duration, err := runner.heartbeatInterval(ctx, assignmentID)
	if err != nil {
		return NewAssignmentActivityError(assignmentID, err)
	}
	keeperID := runner.newID()
	if keeperID == "" {
		return NewAssignmentActivityError(assignmentID, errors.New("activity keeper ID must not be empty"))
	}
	if _, err := runner.lifecycle.BeginAssignmentActivity(ctx, assignmentID, keeperID, duration*2); err != nil {
		return NewAssignmentActivityError(assignmentID, err)
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	workDone := make(chan error, 1)
	go func() { workDone <- operation(workCtx) }()

	ticker := runner.ticker(duration)
	if ticker == nil {
		cancelWork()
		operationErr := <-workDone
		cleanupErr := runner.finish(context.WithoutCancel(ctx), assignmentID, keeperID)
		return joinActivityErrors(assignmentID, operationErr, cleanupErr)
	}
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	var operationErr error
	var heartbeatErr error
	completed := false
	for !completed && heartbeatErr == nil {
		select {
		case operationErr = <-workDone:
			completed = true
			cancelWork()
		case <-ctx.Done():
			cancelWork()
			operationErr = <-workDone
			completed = true
		case <-ticker.C():
			if refreshErr := runner.refresh(ctx, assignmentID, keeperID, duration); refreshErr != nil {
				heartbeatErr = NewAssignmentActivityError(assignmentID, refreshErr)
				cancelWork()
				operationErr = <-workDone
				completed = true
				continue
			}
			newDuration, durationErr := runner.heartbeatInterval(ctx, assignmentID)
			if durationErr != nil {
				heartbeatErr = NewAssignmentActivityError(assignmentID, durationErr)
				cancelWork()
				operationErr = <-workDone
				completed = true
				continue
			}
			if newDuration != duration {
				ticker.Stop()
				duration = newDuration
				ticker = runner.ticker(duration)
				if ticker == nil {
					heartbeatErr = NewAssignmentActivityError(assignmentID, errors.New("activity ticker factory returned nil"))
					cancelWork()
					operationErr = <-workDone
					completed = true
				}
			}
		}
	}
	cleanupErr := runner.finish(context.WithoutCancel(ctx), assignmentID, keeperID)
	if heartbeatErr != nil {
		return joinActivityErrors(assignmentID, errors.Join(heartbeatErr, operationErr), cleanupErr)
	}
	return joinActivityErrors(assignmentID, operationErr, cleanupErr)
}

func (runner *LifecycleActivityRunner) heartbeatInterval(ctx context.Context, assignmentID string) (time.Duration, error) {
	current, err := runner.reader.Read(ctx)
	if err != nil {
		return 0, err
	}
	if current == nil {
		return 0, errors.New("activity state reader returned nil state")
	}
	for _, workspace := range current.Workspaces {
		if workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
			continue
		}
		return ActivityHeartbeatInterval(workspace.Assignment.LeaseDurationMinutes)
	}
	return 0, fmt.Errorf("Assignment %s does not exist", assignmentID)
}

// ActivityHeartbeatInterval applies the bounded one-third lease policy used
// by the production activity runner.
func ActivityHeartbeatInterval(leaseDurationMinutes float64) (time.Duration, error) {
	if math.IsNaN(leaseDurationMinutes) || math.IsInf(leaseDurationMinutes, 0) || leaseDurationMinutes <= 0 {
		return 0, errors.New("leaseDurationMinutes must be positive and finite")
	}
	if leaseDurationMinutes >= maxActivityHeartbeatInterval.Minutes()*3 {
		return maxActivityHeartbeatInterval, nil
	}
	interval := time.Duration(leaseDurationMinutes * float64(time.Minute) / 3)
	if interval <= 0 {
		interval = time.Millisecond
	}
	if interval > maxActivityHeartbeatInterval {
		interval = maxActivityHeartbeatInterval
	}
	return interval, nil
}

func (runner *LifecycleActivityRunner) refresh(ctx context.Context, assignmentID, keeperID string, interval time.Duration) error {
	now := runner.now().UTC()
	_, err := runner.lifecycle.RefreshAssignmentActivity(ctx, assignmentID, keeperID, now.Add(interval*2))
	return err
}

func (runner *LifecycleActivityRunner) finish(ctx context.Context, assignmentID, keeperID string) error {
	_, err := runner.lifecycle.FinishAssignmentActivity(ctx, assignmentID, keeperID)
	if err != nil && (strings.Contains(err.Error(), "Assignment "+assignmentID+" does not exist") || strings.Contains(err.Error(), " is returning, expected assigned") || strings.Contains(err.Error(), " is available, expected assigned")) {
		// Exec may release the assignment immediately after the child record is
		// removed. The exact keeper cleanup was attempted; there is no owner left
		// to mutate, so this expected handoff race is already safely resolved.
		return nil
	}
	return err
}

func joinActivityErrors(assignmentID string, operationErr, cleanupErr error) error {
	if cleanupErr == nil {
		return operationErr
	}
	activityErr := NewAssignmentActivityError(assignmentID, cleanupErr)
	return errors.Join(operationErr, activityErr)
}

func randomActivityUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return ""
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
