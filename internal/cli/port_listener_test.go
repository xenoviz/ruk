package cli

import (
	"fmt"
	"net"
	"testing"

	"github.com/xenoviz/ruk/internal/ports"
)

func TestListenPortHonorsIPv6OnlyFalse(t *testing.T) {
	listener, err := listenPort(ports.BindRequest{
		Network: "tcp6", Address: "[::]:0", IPv6Only: false,
	})
	if err != nil {
		if ports.IPv6Unavailable(err) {
			t.Skipf("IPv6 unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	only, err := ipv6OnlyValue(listener)
	if err != nil {
		t.Fatal(err)
	}
	if only != 0 {
		t.Fatalf("IPV6_V6ONLY = %d, want 0", only)
	}
}

func TestListenPortHonorsIPv6OnlyTrue(t *testing.T) {
	listener, err := listenPort(ports.BindRequest{
		Network: "tcp6", Address: "[::]:0", IPv6Only: true,
	})
	if err != nil {
		if ports.IPv6Unavailable(err) {
			t.Skipf("IPv6 unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	only, err := ipv6OnlyValue(listener)
	if err != nil {
		t.Fatal(err)
	}
	if only != 1 {
		t.Fatalf("IPV6_V6ONLY = %d, want 1", only)
	}
}

func ipv6OnlyValue(listener net.Listener) (int, error) {
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		return 0, fmt.Errorf("listener type %T does not expose TCP socket", listener)
	}
	connection, err := tcpListener.SyscallConn()
	if err != nil {
		return 0, err
	}
	var value int
	var controlErr error
	if err := connection.Control(func(fd uintptr) {
		value, controlErr = readIPv6Only(fd)
	}); err != nil {
		return 0, err
	}
	if controlErr != nil {
		return 0, controlErr
	}
	return value, nil
}
