package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/xenoviz/ruk/internal/lifecycle"
	"github.com/xenoviz/ruk/internal/ports"
	processpkg "github.com/xenoviz/ruk/internal/process"
	"github.com/xenoviz/ruk/internal/state"
)

func defaultPortRegistry(store *state.Store, statePath string) (lifecycle.PortAllocator, lifecycle.ReleasePorter, error) {
	registry, err := ports.NewRegistry(ports.RegistryOptions{})
	if err != nil {
		return nil, nil, err
	}
	probe := ports.NewAvailabilityProbe(func(request ports.BindRequest) (ports.BoundListener, error) {
		listener, err := listenPort(request)
		if err != nil {
			return nil, err
		}
		return netBoundListener{listener: listener}, nil
	})
	allocator := ports.AllocationService{Store: store, Registry: registry, Finder: probe, StatePath: statePath}
	return allocator, registry, nil
}

type netBoundListener struct{ listener net.Listener }

func (listener netBoundListener) Port() int    { return listener.listener.Addr().(*net.TCPAddr).Port }
func (listener netBoundListener) Close() error { return listener.listener.Close() }

func defaultReleaseProcesses() lifecycle.ReleaseProcesser {
	return processpkg.NewNativeProcessManager()
}

func defaultPoolPath(repositoryRoot, branch string) (string, error) {
	if repositoryRoot == "" || branch == "" {
		return "", errors.New("repository root and branch are required")
	}
	return filepath.Join(filepath.Dir(repositoryRoot), filepath.Base(repositoryRoot)+"-ruk-"+slugMutation(branch)+"-"+randomMutationID()[:8]), nil
}

func slugMutation(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			builder.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	value = strings.Trim(builder.String(), "-")
	if value == "" {
		return "workspace"
	}
	return value
}

func randomMutationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		fallback := time.Now().UnixNano()
		for index := range value {
			value[index] = byte(fallback >> (index % 8 * 8))
		}
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
