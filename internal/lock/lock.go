// Package lock provides filesystem-backed directory locks for Ruk state and
// workspace lifecycle transactions.
package lock

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultTimeout = 10 * time.Minute
	defaultStale   = 30 * time.Minute
)

// Options controls lock acquisition waits and abandoned-owner recovery.
type Options struct {
	Timeout time.Duration
	Stale   time.Duration
}

// Owner is the durable identity fence written inside a lock directory.
type Owner struct {
	PID             int    `json:"pid"`
	Hostname        string `json:"hostname"`
	Token           string `json:"token"`
	CreatedAt       string `json:"createdAt"`
	ProcessIdentity string `json:"processIdentity,omitempty"`
}

// ProcessState describes the strongest process identity evidence available to
// a platform-specific probe. An alive process with unknown identity is treated
// as live and can never be displaced merely because the lock is old.
type ProcessState struct {
	Alive         bool
	IdentityKnown bool
	Identity      string
}

// IdentityMatch describes compatibility between a persisted process identity
// and a fresh native observation. Legacy POSIX identities are second-rounded
// timestamps; they remain liveness-compatible with a Linux raw-tick identity,
// but are never an exact fence for signaling or termination.
type IdentityMatch uint8

const (
	IdentityMismatch IdentityMatch = iota
	IdentityExact
	IdentityLegacyCompatible
)

func CompareIdentity(expected, observed string) IdentityMatch {
	if expected == "" || observed == "" {
		return IdentityMismatch
	}
	if expected == observed {
		return IdentityExact
	}
	if (isLegacyPOSIXIdentity(expected) && strings.HasPrefix(observed, "linux:")) || (isLegacyPOSIXIdentity(observed) && strings.HasPrefix(expected, "linux:")) {
		return IdentityLegacyCompatible
	}
	return IdentityMismatch
}

func isLegacyPOSIXIdentity(value string) bool {
	_, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", value, time.Local)
	return err == nil
}

// ProcessProbe inspects a local lock owner without launching helper processes.
type ProcessProbe interface {
	Inspect(ctx context.Context, pid int) (ProcessState, error)
}

// Config supplies platform identity and deterministic seams to DirectoryLocker.
type Config struct {
	Options         Options
	PID             int
	Hostname        string
	ProcessIdentity string
	Probe           ProcessProbe
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
	Token           func() string
}

// TimeoutError reports that a lock remained owned until the configured wait
// expired. Callers can use errors.As rather than parsing the message.
type TimeoutError struct {
	Path    string
	Timeout time.Duration
}

func (err *TimeoutError) Error() string {
	return fmt.Sprintf("timed out waiting for lock %s", err.Path)
}

// DirectoryLocker serializes callbacks with an owner-fenced directory lock.
type DirectoryLocker struct {
	options         Options
	pid             int
	hostname        string
	processIdentity string
	probe           ProcessProbe
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error
	token           func() string
}

// NewDirectoryLocker creates a lock manager. A missing process probe fails
// closed for same-host owners, leaving native identity inspection to the
// platform integration layer.
func NewDirectoryLocker(config Config) *DirectoryLocker {
	if config.Options.Timeout <= 0 {
		config.Options.Timeout = defaultTimeout
	}
	if config.Options.Stale <= 0 {
		config.Options.Stale = defaultStale
	}
	if config.PID <= 0 {
		config.PID = os.Getpid()
	}
	if config.Hostname == "" {
		config.Hostname, _ = os.Hostname()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Sleep == nil {
		config.Sleep = sleepContext
	}
	if config.Token == nil {
		config.Token = randomToken
	}
	if config.Probe == nil {
		config.Probe = unknownProcessProbe{}
	}
	return &DirectoryLocker{
		options:         config.Options,
		pid:             config.PID,
		hostname:        config.Hostname,
		processIdentity: config.ProcessIdentity,
		probe:           config.Probe,
		now:             config.Now,
		sleep:           config.Sleep,
		token:           config.Token,
	}
}

// With acquires path, executes fn, and releases only the lock still carrying
// this acquisition's opaque owner token.
func (locker *DirectoryLocker) With(ctx context.Context, path string, fn func() error) (result error) {
	if fn == nil {
		return errors.New("lock: nil callback")
	}
	guard, err := locker.Acquire(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		result = errors.Join(result, guard.Release())
	}()
	return fn()
}

// Acquire waits for exclusive ownership of path or returns a typed timeout or
// context error. Abandoned locks are moved to unique tombstones, never deleted
// in place under contention.
func (locker *DirectoryLocker) Acquire(ctx context.Context, path string) (*Guard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("lock: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock parent %s: %w", filepath.Dir(path), err)
	}
	cleanupReleasedTombstones(path)

	started := locker.now()
	token := locker.token()
	if token == "" {
		return nil, errors.New("lock: empty owner token")
	}
	owner := Owner{
		PID:             locker.pid,
		Hostname:        locker.hostname,
		Token:           token,
		CreatedAt:       formatTimestamp(locker.now()),
		ProcessIdentity: locker.processIdentity,
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := os.Mkdir(path, 0o700)
		if err == nil {
			if err := writeOwner(path, owner); err != nil {
				_ = os.RemoveAll(path)
				return nil, err
			}
			return &Guard{path: path, token: token}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create lock %s: %w", path, err)
		}

		recovered, err := locker.recoverAbandoned(ctx, path)
		if err != nil {
			return nil, err
		}
		if recovered {
			continue
		}

		elapsed := locker.now().Sub(started)
		if elapsed >= locker.options.Timeout {
			return nil, &TimeoutError{Path: path, Timeout: locker.options.Timeout}
		}
		remaining := locker.options.Timeout - elapsed
		delay := retryDelay()
		if delay > remaining {
			delay = remaining
		}
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
		if err := locker.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (locker *DirectoryLocker) recoverAbandoned(ctx context.Context, path string) (bool, error) {
	stat, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return false, nil
		}
		return false, fmt.Errorf("inspect lock %s: %w", path, err)
	}
	age := locker.now().Sub(stat.ModTime())
	if age <= locker.options.Stale {
		return false, nil
	}

	owner, valid, err := readOwner(path)
	if err != nil {
		// Unreadable owner metadata is not evidence that the owner is dead.
		return false, nil
	}
	if !valid {
		// A readable but malformed owner record is not evidence that the
		// process holding this lock is gone. Keep it fenced indefinitely;
		// recovery by age alone could race a live owner.
		return false, nil
	}
	if valid && owner.Hostname == locker.hostname {
		alive, err := locker.ownerIsAlive(ctx, owner)
		if err != nil || alive {
			return false, nil
		}
	}

	identity := abandonedIdentity(path, stat.ModTime(), owner, valid)
	tombstone := path + ".abandoned-" + identity
	if err := os.Rename(path, tombstone); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist) || errors.Is(err, os.ErrPermission) {
			return false, nil
		}
		return false, fmt.Errorf("recover abandoned lock %s: %w", path, err)
	}
	return true, nil
}

func (locker *DirectoryLocker) ownerIsAlive(ctx context.Context, owner Owner) (bool, error) {
	if owner.PID <= 0 {
		return false, nil
	}
	state, err := locker.probe.Inspect(ctx, owner.PID)
	if err != nil {
		return true, err
	}
	if !state.Alive {
		return false, nil
	}
	if owner.ProcessIdentity == "" || !state.IdentityKnown {
		return true, nil
	}
	match := CompareIdentity(owner.ProcessIdentity, state.Identity)
	return match == IdentityExact || match == IdentityLegacyCompatible, nil
}

// Guard represents one acquired directory lock.
type Guard struct {
	path         string
	token        string
	releasedPath string
}

// Release removes the lock only when its owner token still belongs to this
// guard. A missing or replaced lock is already released from this guard's view.
func (guard *Guard) Release() error {
	if guard.releasedPath != "" {
		if err := os.RemoveAll(guard.releasedPath); err != nil {
			return fmt.Errorf("cleanup released lock %s: %w", guard.path, err)
		}
		guard.releasedPath = ""
		return nil
	}
	owner, valid, err := readOwner(guard.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read lock owner %s: %w", guard.path, err)
	}
	if !valid || owner.Token != guard.token {
		return nil
	}
	// Move the token-verified lock directory out of the canonical path before
	// cleanup. This makes logical release durable when recursive cleanup fails:
	// future contenders can acquire the canonical path, while the tombstone is
	// safe to retry or garbage-collect later. Verify the moved owner again so a
	// replacement owner cannot be accidentally moved or removed by a stale
	// guard racing this release. Within one host, an exact live owner remains
	// fenced by its token while this guard runs; this does not claim a
	// cross-host serialization guarantee for shared filesystems.
	releasedPath := guard.path + ".released-" + releaseToken(guard.token)
	if err := os.Rename(guard.path, releasedPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("logically release lock %s: %w", guard.path, err)
	}
	guard.releasedPath = releasedPath
	movedOwner, movedValid, movedErr := readOwner(releasedPath)
	if movedErr != nil || !movedValid || movedOwner.Token != guard.token {
		verifyErr := movedErr
		if verifyErr == nil {
			verifyErr = errors.New("owner token changed or metadata is invalid")
		}
		// The rename raced a replacement or corrupted metadata. Restore only if
		// the canonical path is still absent; never overwrite a newer owner.
		if _, statErr := os.Stat(guard.path); errors.Is(statErr, os.ErrNotExist) {
			if restoreErr := os.Rename(releasedPath, guard.path); restoreErr != nil {
				return errors.Join(fmt.Errorf("verify released lock %s: %w", guard.path, verifyErr), restoreErr)
			}
		}
		return fmt.Errorf("verify released lock %s: %w", guard.path, verifyErr)
	}
	if err := os.RemoveAll(releasedPath); err != nil {
		return fmt.Errorf("cleanup released lock %s: %w", guard.path, err)
	}
	guard.releasedPath = ""
	return nil
}

// cleanupReleasedTombstones removes only directories produced by the
// token-fenced Guard.Release handoff for this canonical lock. They no longer
// own the canonical path, so cleanup cannot touch a replacement owner.
func cleanupReleasedTombstones(path string) {
	parent := filepath.Dir(path)
	prefix := filepath.Base(path) + ".released-"
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
	}
}

func releaseToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])[:20]
}

func writeOwner(path string, owner Owner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return fmt.Errorf("encode lock owner %s: %w", path, err)
	}
	ownerPath := filepath.Join(path, "owner.json")
	file, err := os.OpenFile(ownerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create lock owner %s: %w", ownerPath, err)
	}
	writeErr := error(nil)
	if _, err := file.Write(data); err != nil {
		writeErr = fmt.Errorf("write lock owner %s: %w", ownerPath, err)
	}
	if err := file.Close(); err != nil && writeErr == nil {
		writeErr = fmt.Errorf("close lock owner %s: %w", ownerPath, err)
	}
	if writeErr != nil {
		return writeErr
	}
	if err := os.Chmod(ownerPath, 0o600); err != nil {
		return fmt.Errorf("secure lock owner %s: %w", ownerPath, err)
	}
	return nil
}

func readOwner(path string) (Owner, bool, error) {
	data, err := os.ReadFile(filepath.Join(path, "owner.json"))
	if err != nil {
		return Owner{}, false, err
	}
	var owner Owner
	if err := json.Unmarshal(data, &owner); err != nil {
		return Owner{}, false, nil
	}
	valid := owner.PID > 0 && owner.Hostname != "" && owner.Token != "" && owner.CreatedAt != ""
	return owner, valid, nil
}

func abandonedIdentity(path string, modified time.Time, owner Owner, valid bool) string {
	value := owner.Token
	if !valid {
		value = fmt.Sprintf("%s:%d", path, modified.UnixNano())
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:20]
}

func randomToken() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(fmt.Sprintf("lock: generate owner token: %v", err))
	}
	return hex.EncodeToString(data[:])
}

func retryDelay() time.Duration {
	var data [1]byte
	if _, err := rand.Read(data[:]); err != nil {
		return 150 * time.Millisecond
	}
	return time.Duration(150+int(data[0])%150) * time.Millisecond
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

type unknownProcessProbe struct{}

func (unknownProcessProbe) Inspect(context.Context, int) (ProcessState, error) {
	return ProcessState{Alive: true, IdentityKnown: false}, nil
}
