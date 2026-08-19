//go:build linux

package cli

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"testing"

	"github.com/xenoviz/ruk/internal/ports"
)

func TestListenPortDualStackRejectsIPv4OccupiedPort(t *testing.T) {
	occupied, err := exclusiveIPv4Listener()
	if err != nil {
		t.Skipf("IPv4 listener unavailable: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	listener, err := listenPort(ports.BindRequest{
		Network: "tcp6", Address: "[::]:" + strconv.Itoa(port), IPv6Only: false,
	})
	if err == nil {
		listener.Close()
		t.Fatal("dual-stack probe ignored an occupied IPv4 port")
	}
	if ports.IPv6Unavailable(err) {
		t.Skipf("IPv6 unavailable: %v", err)
	}
}

func exclusiveIPv4Listener() (net.Listener, error) {
	config := net.ListenConfig{
		Control: func(_ string, _ string, connection syscall.RawConn) error {
			var controlErr error
			if err := connection.Control(func(fd uintptr) {
				controlErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 0)
			}); err != nil {
				return err
			}
			return controlErr
		},
	}
	return config.Listen(context.Background(), "tcp4", "127.0.0.1:0")
}
