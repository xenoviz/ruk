// Package ports contains the host-local named-port allocation primitives.
//
// Durable reservations are deliberately outside this package's first slice.
// Callers can validate names, construct a child-process environment, and
// probe an ephemeral port before recording an assignment under its state lock.
package ports

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"syscall"
)

const (
	// EnvironmentPrefix is the prefix used for named-port variables.
	EnvironmentPrefix = "RUK_PORT_"
	minPort           = int64(1)
	maxPort           = int64(65_535)
	defaultAttempts   = 20
)

// ErrIPv6Unavailable tells an availability probe to use its IPv4 fallback.
// A listener implementation may wrap this sentinel with additional context.
var ErrIPv6Unavailable = errors.New("IPv6 is unavailable")

// NormalizeName converts a user-facing name to its RUK environment variable.
// Invalid runs collapse to one underscore, matching the TypeScript behavior;
// names without an ASCII letter or digit are rejected.
func NormalizeName(name string) (string, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	var normalized strings.Builder
	lastUnderscore := false
	for _, character := range name {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			normalized.WriteRune(character)
			lastUnderscore = false
			continue
		}
		if normalized.Len() > 0 && !lastUnderscore {
			normalized.WriteByte('_')
			lastUnderscore = true
		}
	}
	value := strings.Trim(normalized.String(), "_")
	if value == "" {
		return "", errors.New("port name must contain a letter or number")
	}
	return EnvironmentPrefix + value, nil
}

// PortEnvironmentName is an explicit alias for NormalizeName at call sites
// that deal with environment variables.
func PortEnvironmentName(name string) (string, error) {
	return NormalizeName(name)
}

// ValidatePort checks the TCP/UDP port range accepted by the state contract.
func ValidatePort(port int64) error {
	if port < minPort || port > maxPort {
		return fmt.Errorf("port %d must be between %d and %d", port, minPort, maxPort)
	}
	return nil
}

// BuildEnvironment constructs the environment additions for named ports.
// Input keys are sorted before processing so validation and duplicate errors
// are deterministic even though the returned map has no iteration contract.
func BuildEnvironment(ports map[string]int64) (map[string]string, error) {
	keys := make([]string, 0, len(ports))
	for name := range ports {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	environment := make(map[string]string, len(ports))
	originalNames := make(map[string]string, len(ports))
	for _, name := range keys {
		port := ports[name]
		if err := ValidatePort(port); err != nil {
			return nil, fmt.Errorf("port %q: %w", name, err)
		}
		environmentName, err := NormalizeName(name)
		if err != nil {
			return nil, fmt.Errorf("port %q: %w", name, err)
		}
		if previous, exists := originalNames[environmentName]; exists {
			return nil, fmt.Errorf("port names %q and %q normalize to duplicate %q", previous, name, environmentName)
		}
		originalNames[environmentName] = name
		environment[environmentName] = fmt.Sprintf("%d", port)
	}
	return environment, nil
}

// PortEnvironment is an explicit alias for BuildEnvironment.
func PortEnvironment(ports map[string]int64) (map[string]string, error) {
	return BuildEnvironment(ports)
}

// BindRequest describes one host-local ephemeral bind attempt. The first
// request is dual-stack IPv6 (IPv6Only false); IPv4 is used only when the
// listener reports that IPv6 cannot be used.
type BindRequest struct {
	Network  string
	Address  string
	IPv6Only bool
}

// BoundListener is the small seam needed by AvailabilityProbe. A production
// adapter can wrap net.Listener; tests can use a no-socket fake.
type BoundListener interface {
	Port() int
	Close() error
}

// ListenFunc opens one ephemeral listener described by a BindRequest.
type ListenFunc func(BindRequest) (BoundListener, error)

// IPv6Unavailable reports errors for which probing should use IPv4. The
// exported function lets platform adapters extend the sentinel behavior.
func IPv6Unavailable(err error) bool {
	return errors.Is(err, ErrIPv6Unavailable) || errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)
}

// AvailabilityProbe finds a host-local ephemeral port while leaving no socket
// open after each attempt. It does not inspect or mutate any durable registry.
type AvailabilityProbe struct {
	Listen          ListenFunc
	IPv6Unavailable func(error) bool
	MaxAttempts     int
}

// NewAvailabilityProbe creates a probe with the dual-stack contract and the
// standard retry bound. Listen must be supplied by the caller.
func NewAvailabilityProbe(listen ListenFunc) AvailabilityProbe {
	return AvailabilityProbe{Listen: listen, IPv6Unavailable: IPv6Unavailable, MaxAttempts: defaultAttempts}
}

// Find returns a probed port not present in excluded.
func (probe AvailabilityProbe) Find(excluded map[int64]struct{}) (int64, error) {
	if probe.Listen == nil {
		return 0, errors.New("port availability listener is required")
	}
	isIPv6Unavailable := probe.IPv6Unavailable
	if isIPv6Unavailable == nil {
		isIPv6Unavailable = IPv6Unavailable
	}
	attempts := probe.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}

	for attempt := 0; attempt < attempts; attempt++ {
		bound, err := probe.Listen(BindRequest{Network: "tcp6", Address: "[::]:0", IPv6Only: false})
		if err != nil {
			if !isIPv6Unavailable(err) {
				return 0, fmt.Errorf("probe IPv6 listener: %w", err)
			}
			bound, err = probe.Listen(BindRequest{Network: "tcp4", Address: "127.0.0.1:0"})
		}
		if err != nil {
			return 0, fmt.Errorf("probe IPv4 listener: %w", err)
		}
		if bound == nil {
			return 0, errors.New("port availability listener returned nil")
		}

		port := int64(bound.Port())
		closeErr := bound.Close()
		if closeErr != nil {
			return 0, fmt.Errorf("close port availability listener: %w", closeErr)
		}
		if err := ValidatePort(port); err != nil {
			return 0, fmt.Errorf("listener returned invalid port: %w", err)
		}
		if _, exists := excluded[port]; exists {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("could not allocate an available port after %d attempts", attempts)
}

// AvailablePort is the function form of NewAvailabilityProbe and Find.
func AvailablePort(excluded map[int64]struct{}, listen ListenFunc) (int64, error) {
	return NewAvailabilityProbe(listen).Find(excluded)
}
