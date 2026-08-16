package cli

import (
	"context"
	"net"
	"syscall"

	"github.com/xenoviz/ruk/internal/ports"
)

// listenPort opens the socket described by a port availability probe. The
// IPv6 request is explicitly configured before bind so IPv6Only=false really
// reserves the corresponding IPv4 address too; net.Listen's platform default
// is not a sufficient contract for the allocator.
func listenPort(request ports.BindRequest) (net.Listener, error) {
	if request.Network != "tcp6" {
		return net.Listen(request.Network, request.Address)
	}
	config := net.ListenConfig{
		Control: func(_ string, _ string, connection syscall.RawConn) error {
			var controlErr error
			if err := connection.Control(func(fd uintptr) {
				controlErr = setIPv6Only(fd, request.IPv6Only)
			}); err != nil {
				return err
			}
			return controlErr
		},
	}
	return config.Listen(context.Background(), request.Network, request.Address)
}
