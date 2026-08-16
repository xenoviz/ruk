package cli

import (
	"net"
	"strconv"
	"testing"

	"github.com/xenoviz/ruk/internal/ports"
)

func TestListenPortDualStackRejectsIPv4OccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
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

func TestListenPortDualStackCanBindWhenBothFamiliesAreFree(t *testing.T) {
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
	port := listener.Addr().(*net.TCPAddr).Port

	ipv4, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err == nil {
		ipv4.Close()
		t.Fatal("IPv6Only=false did not reserve the IPv4 family")
	}
}
