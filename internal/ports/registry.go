package ports

// This file owns the durable, host-local named-port reservation registry. A
// registry reservation is cooperative metadata: it does not hold a socket and
// therefore cannot reserve a port from unrelated processes.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xenoviz/ruk/internal/lock"
	"github.com/xenoviz/ruk/internal/state"
)

const (
	registryVersion  = 1
	registryFileName = "ports.json"
	registryLockName = "ports.lock"
)

// RegistryReservation is one durable port owner. StatePath identifies the
// repository state that remains the source of truth for assignment liveness.
type RegistryReservation struct {
	AssignmentID string `json:"assignmentId"`
	StatePath    string `json:"statePath"`
}

type hostPortRegistry struct {
	Version int                            `json:"version"`
	Ports   map[string]RegistryReservation `json:"ports"`
}

// RegistryLocker is intentionally the same shape as state.Locker. Keeping the
// boundary small allows tests and callers to inject an in-process serializer,
// while production uses lock.DirectoryLocker for cross-process fencing.
type RegistryLocker interface {
	With(context.Context, string, func() error) error
}

// RegistryFile is the small write boundary needed for exclusive temporary
// files. The OS implementation is backed by OpenFile(O_CREATE|O_EXCL).
type RegistryFile interface {
	io.Writer
	io.Closer
	Sync() error
}

// RegistryFileSystem keeps filesystem effects injectable without introducing a
// third-party filesystem dependency into the runtime.
type RegistryFileSystem interface {
	MkdirAll(string, os.FileMode) error
	Lstat(string) (os.FileInfo, error)
	ReadFile(string) ([]byte, error)
	OpenExclusive(string, os.FileMode) (RegistryFile, error)
	Rename(string, string) error
	Remove(string) error
	Chmod(string, os.FileMode) error
}

type osRegistryFileSystem struct{}

func (osRegistryFileSystem) MkdirAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (osRegistryFileSystem) Lstat(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

func (osRegistryFileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osRegistryFileSystem) OpenExclusive(path string, mode os.FileMode) (RegistryFile, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func (osRegistryFileSystem) Rename(oldPath, newPath string) error {
	return replaceRegistryFile(oldPath, newPath)
}
func (osRegistryFileSystem) Remove(path string) error { return os.Remove(path) }
func (osRegistryFileSystem) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

// ActivityChecker answers whether one registry reservation is still backed by
// an active assignment. An error is fail-closed: the registry is not changed.
type ActivityChecker interface {
	Active(context.Context, string, string, int64) (bool, error)
}

// StateActivity reads and validates canonical Ruk state files. Missing state
// is treated as stale; malformed state is treated as corruption and returned.
type StateActivity struct {
	Files RegistryFileSystem
}

func (activity StateActivity) Active(ctx context.Context, statePath, assignmentID string, port int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	files := activity.Files
	if files == nil {
		files = osRegistryFileSystem{}
	}
	data, err := files.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read assignment state %s: %w", statePath, err)
	}
	decoded, err := state.Decode(data, statePath)
	if err != nil {
		return false, err
	}
	for _, workspace := range decoded.Workspaces {
		if workspace.Assignment == nil || workspace.Assignment.ID != assignmentID {
			continue
		}
		for _, assignedPort := range workspace.Assignment.Ports {
			if assignedPort == port {
				return true, nil
			}
		}
	}
	return false, nil
}

// RegistryOptions configures a host registry. Root is intended for tests and
// controlled integrations; leaving it empty selects DefaultRegistryRoot.
type RegistryOptions struct {
	Root     string
	Locker   RegistryLocker
	Files    RegistryFileSystem
	Activity ActivityChecker
}

// Registry serializes and commits host-port reservations below one stable root.
type Registry struct {
	root     string
	locker   RegistryLocker
	files    RegistryFileSystem
	activity ActivityChecker
}

// NewRegistry constructs a registry with production-safe defaults.
func NewRegistry(options RegistryOptions) (*Registry, error) {
	root := options.Root
	if root == "" {
		var err error
		root, err = DefaultRegistryRoot()
		if err != nil {
			return nil, err
		}
	}
	files := options.Files
	if files == nil {
		files = osRegistryFileSystem{}
	}
	locker := options.Locker
	if locker == nil {
		locker = lock.NewDirectoryLocker(lock.Config{})
	}
	activity := options.Activity
	if activity == nil {
		activity = StateActivity{Files: files}
	}
	return &Registry{root: root, locker: locker, files: files, activity: activity}, nil
}

// DefaultRegistryRoot returns a stable per-user path, deliberately avoiding
// os.TempDir. The fallback only applies when the platform cannot report a
// home directory, in which case UserConfigDir is the least surprising choice.
func DefaultRegistryRoot() (string, error) {
	home, homeErr := os.UserHomeDir()
	if homeErr == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".config", "ruk", "host"), nil
	}
	config, configErr := os.UserConfigDir()
	if configErr != nil {
		if homeErr != nil {
			return "", fmt.Errorf("resolve per-user registry root: %w", homeErr)
		}
		return "", fmt.Errorf("resolve per-user registry root: %w", configErr)
	}
	return filepath.Join(config, "ruk", "host"), nil
}

// Root reports the configured durable root. It is useful to diagnostics and
// does not expose mutable registry state.
func (registry *Registry) Root() string { return registry.root }

// ReservationTransaction is valid only during the callback passed to With.
// Its private fence prevents delayed commit/release calls from crossing a
// completed transaction or another lock owner.
type ReservationTransaction struct {
	registry     *Registry
	file         string
	reservations map[int64]RegistryReservation
	active       bool
	committed    bool
}

// Reserved returns a snapshot of all ports occupied after stale pruning.
func (transaction *ReservationTransaction) Reserved() map[int64]struct{} {
	result := make(map[int64]struct{}, len(transaction.reservations))
	for port := range transaction.reservations {
		result[port] = struct{}{}
	}
	return result
}

// Reserve adds or replaces the reservation for port when it is already owned
// by assignmentID. A different active assignment can never be overwritten.
func (transaction *ReservationTransaction) Reserve(port int64, assignmentID, statePath string) error {
	if err := transaction.checkOpen(); err != nil {
		return err
	}
	if err := ValidatePort(port); err != nil {
		return err
	}
	if strings.TrimSpace(assignmentID) == "" {
		return errors.New("port reservation assignment ID is required")
	}
	if !filepath.IsAbs(statePath) {
		return errors.New("port reservation state path must be absolute")
	}
	if existing, ok := transaction.reservations[port]; ok && existing.AssignmentID != assignmentID {
		return fmt.Errorf("port %d is already reserved by assignment %s", port, existing.AssignmentID)
	}
	transaction.reservations[port] = RegistryReservation{AssignmentID: assignmentID, StatePath: statePath}
	return nil
}

// Release removes every port fenced by assignmentID. An unknown assignment is
// a safe no-op, which makes retries idempotent while preventing stale IDs from
// affecting a newly assigned workspace.
func (transaction *ReservationTransaction) Release(assignmentID string) error {
	if err := transaction.checkOpen(); err != nil {
		return err
	}
	if strings.TrimSpace(assignmentID) == "" {
		return errors.New("port reservation assignment ID is required")
	}
	for port, reservation := range transaction.reservations {
		if reservation.AssignmentID == assignmentID {
			delete(transaction.reservations, port)
		}
	}
	return nil
}

// Commit persists the transaction immediately. With automatically commits a
// successful callback, so explicit Commit is needed only to fence a commit
// before callback return or to make the operation's intent obvious.
func (transaction *ReservationTransaction) Commit() error {
	if err := transaction.checkOpen(); err != nil {
		return err
	}
	if transaction.committed {
		return errors.New("port registry transaction is already committed")
	}
	if err := transaction.registry.write(transaction.file, transaction.registryValue()); err != nil {
		return err
	}
	transaction.committed = true
	return nil
}

func (transaction *ReservationTransaction) checkOpen() error {
	if transaction == nil || !transaction.active {
		return errors.New("port registry transaction is no longer active")
	}
	if transaction.committed {
		return errors.New("port registry transaction is already committed")
	}
	return nil
}

func (transaction *ReservationTransaction) registryValue() hostPortRegistry {
	ports := make(map[string]RegistryReservation, len(transaction.reservations))
	for port, reservation := range transaction.reservations {
		ports[strconv.FormatInt(port, 10)] = reservation
	}
	return hostPortRegistry{Version: registryVersion, Ports: ports}
}

// With serializes one registry transaction, prunes stale reservations, and
// atomically commits all changes from a successful callback.
func (registry *Registry) With(ctx context.Context, callback func(*ReservationTransaction) error) error {
	if registry == nil || registry.locker == nil || registry.files == nil || registry.activity == nil {
		return errors.New("port registry is not configured")
	}
	if callback == nil {
		return errors.New("port registry callback is required")
	}
	if err := ensureRegistryRoot(registry.files, registry.root); err != nil {
		return err
	}
	file := filepath.Join(registry.root, registryFileName)
	lockPath := filepath.Join(registry.root, registryLockName)
	return registry.locker.With(ctx, lockPath, func() error {
		current, err := registry.read(file)
		if err != nil {
			return err
		}
		for portText, reservation := range current.Ports {
			port, parseErr := strconv.ParseInt(portText, 10, 64)
			if parseErr != nil {
				return fmt.Errorf("invalid port registry key %q: %w", portText, parseErr)
			}
			active, activityErr := registry.activity.Active(ctx, reservation.StatePath, reservation.AssignmentID, port)
			if activityErr != nil {
				return activityErr
			}
			if !active {
				delete(current.Ports, portText)
			}
		}
		reservations := make(map[int64]RegistryReservation, len(current.Ports))
		for portText, reservation := range current.Ports {
			port, _ := strconv.ParseInt(portText, 10, 64)
			reservations[port] = reservation
		}
		transaction := &ReservationTransaction{registry: registry, file: file, reservations: reservations, active: true}
		defer func() { transaction.active = false }()
		if err := callback(transaction); err != nil {
			return err
		}
		if transaction.committed {
			return nil
		}
		return transaction.Commit()
	})
}

// WithReservations adapts the concrete durable registry transaction to the
// narrow interface used by AllocationService.
func (registry *Registry) WithReservations(ctx context.Context, callback func(ReservationSession) error) error {
	if callback == nil {
		return errors.New("port reservation callback is required")
	}
	return registry.With(ctx, func(transaction *ReservationTransaction) error {
		return callback(transaction)
	})
}

// Release removes all ports owned by assignmentID through a fenced registry
// transaction. It is safe to retry after a successful release.
func (registry *Registry) Release(ctx context.Context, assignmentID string) error {
	return registry.With(ctx, func(transaction *ReservationTransaction) error {
		return transaction.Release(assignmentID)
	})
}

func (registry *Registry) read(file string) (hostPortRegistry, error) {
	info, err := registry.files.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return hostPortRegistry{Version: registryVersion, Ports: map[string]RegistryReservation{}}, nil
	}
	if err != nil {
		return hostPortRegistry{}, fmt.Errorf("inspect port registry %s: %w", file, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return hostPortRegistry{}, fmt.Errorf("unsafe Ruk host port registry file %s", file)
	}
	if err := verifyRegistryFileOwner(info, file); err != nil {
		return hostPortRegistry{}, fmt.Errorf("unsafe Ruk host port registry file %s: %w", file, err)
	}
	data, err := registry.files.ReadFile(file)
	if err != nil {
		return hostPortRegistry{}, fmt.Errorf("read port registry %s: %w", file, err)
	}
	return decodeRegistry(data, file)
}

func (registry *Registry) write(file string, value hostPortRegistry) (result error) {
	encoded, err := encodeRegistry(value)
	if err != nil {
		return err
	}
	temporary := file + "." + randomRegistryToken() + ".tmp"
	writer, err := registry.files.OpenExclusive(temporary, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary port registry %s: %w", temporary, err)
	}
	committed := false
	closed := false
	defer func() {
		if !closed {
			closeErr := writer.Close()
			closed = true
			if result == nil && closeErr != nil {
				result = fmt.Errorf("close temporary port registry %s: %w", temporary, closeErr)
			}
		}
		if !committed {
			if removeErr := registry.files.Remove(temporary); result == nil && !errors.Is(removeErr, os.ErrNotExist) {
				result = fmt.Errorf("remove temporary port registry %s: %w", temporary, removeErr)
			}
		}
	}()
	written, err := writer.Write(encoded)
	if err != nil {
		return fmt.Errorf("write temporary port registry %s: %w", temporary, err)
	}
	if written != len(encoded) {
		return fmt.Errorf("write temporary port registry %s: %w", temporary, io.ErrShortWrite)
	}
	if err := writer.Sync(); err != nil {
		return fmt.Errorf("sync temporary port registry %s: %w", temporary, err)
	}
	if closeErr := writer.Close(); closeErr != nil {
		closed = true
		result = fmt.Errorf("close temporary port registry %s: %w", temporary, closeErr)
		return result
	}
	closed = true
	if err := registry.files.Chmod(temporary, 0o600); err != nil {
		return fmt.Errorf("secure temporary port registry %s: %w", temporary, err)
	}
	if err := registry.files.Rename(temporary, file); err != nil {
		return fmt.Errorf("replace port registry %s: %w", file, err)
	}
	committed = true
	return nil
}

func ensureRegistryRoot(files RegistryFileSystem, root string) error {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return errors.New("port registry root must be an absolute path")
	}
	// Validate every existing component before creating anything. MkdirAll
	// follows an existing junction/symlink, so checking only the final path
	// after creation would allow a registry to be placed beneath an attacker-
	// controlled parent. The second pass below closes the same gap for newly
	// created components and detects a reparse point introduced during setup.
	if err := validateRegistryRootPath(files, root); err != nil {
		return fmt.Errorf("Unsafe Ruk host port directory %s: %w", root, err)
	}
	if err := files.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create port registry root %s: %w", root, err)
	}
	if err := validateRegistryRootPath(files, root); err != nil {
		return fmt.Errorf("Unsafe Ruk host port directory %s: %w", root, err)
	}
	info, err := files.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect port registry root %s: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("Unsafe Ruk host port directory %s", root)
	}
	if err := verifyRegistryRootOwner(info, root); err != nil {
		return fmt.Errorf("Unsafe Ruk host port directory %s: %w", root, err)
	}
	return nil
}

func validateRegistryRootPath(files RegistryFileSystem, root string) error {
	cleanRoot := filepath.Clean(root)
	for current := cleanRoot; ; current = filepath.Dir(current) {
		info, err := files.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if filepath.Dir(current) == current {
				return nil
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect port registry path %s: %w", current, err)
		}
		if err := verifyRegistryRootPathComponent(info, current); err != nil {
			return fmt.Errorf("unsafe port registry path %s: %w", current, err)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func encodeRegistry(value hostPortRegistry) ([]byte, error) {
	if err := validateRegistry(value); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode port registry: %w", err)
	}
	return append(data, '\n'), nil
}

func decodeRegistry(data []byte, source string) (hostPortRegistry, error) {
	if err := validateJSONDocument(data); err != nil {
		return hostPortRegistry{}, fmt.Errorf("Cannot parse Ruk host port registry in %s: %w", source, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var value hostPortRegistry
	if err := decoder.Decode(&value); err != nil {
		return hostPortRegistry{}, fmt.Errorf("Cannot parse Ruk host port registry in %s: %w", source, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return hostPortRegistry{}, fmt.Errorf("Unsupported or invalid Ruk host port registry in %s", source)
		}
		return hostPortRegistry{}, fmt.Errorf("Cannot parse Ruk host port registry in %s: %w", source, err)
	}
	if err := validateRegistry(value); err != nil {
		return hostPortRegistry{}, fmt.Errorf("Unsupported or invalid Ruk host port registry in %s: %w", source, err)
	}
	return value, nil
}

// validateJSONDocument rejects duplicate object keys before decoding into Go
// structs (encoding/json otherwise silently keeps the last duplicate value).
func validateJSONDocument(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			keys[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			if err != nil {
				return err
			}
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			if err != nil {
				return err
			}
			return errors.New("unterminated JSON array")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func validateRegistry(value hostPortRegistry) error {
	if value.Version != registryVersion || value.Ports == nil {
		return errors.New("registry version or ports map is invalid")
	}
	for portText, reservation := range value.Ports {
		if portText == "" || (len(portText) > 1 && portText[0] == '0') {
			return fmt.Errorf("invalid port key %q", portText)
		}
		port, err := strconv.ParseInt(portText, 10, 64)
		if err != nil || strconv.FormatInt(port, 10) != portText || ValidatePort(port) != nil {
			return fmt.Errorf("invalid port key %q", portText)
		}
		if strings.TrimSpace(reservation.AssignmentID) == "" || !filepath.IsAbs(reservation.StatePath) {
			return fmt.Errorf("invalid reservation for port %d", port)
		}
	}
	return nil
}

func randomRegistryToken() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return strconv.FormatInt(int64(os.Getpid()), 10)
	}
	return hex.EncodeToString(data[:])
}
