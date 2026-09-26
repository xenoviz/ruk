package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/xenoviz/ruk/internal/dashboard"
)

// UIListenFunc opens the dashboard listener. Tests inject one to observe the
// address without binding a fixed port.
type UIListenFunc func(network, address string) (net.Listener, error)

// UIOpenFunc opens the dashboard address in a browser.
type UIOpenFunc func(url string) error

type uiListening struct {
	Status  string `json:"status"`
	Address string `json:"address"`
	URL     string `json:"url"`
}

// runUI serves the local dashboard until interrupted. It is a foreground
// command, not a daemon: it renews nothing on its own, and every change it
// makes is an explicit action that runs the matching Ruk command.
func (application *Application) runUI(ctx context.Context, invocation Invocation) (int, error) {
	port, err := parseUIPort(invocation.Ports)
	if err != nil {
		return 1, err
	}
	listen := application.uiListen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return 1, fmt.Errorf("start dashboard on port %d: %w", port, err)
	}
	defer listener.Close()
	token, err := dashboard.NewToken()
	if err != nil {
		return 1, err
	}
	address := listener.Addr().String()
	handler, err := dashboard.NewHandler(newUISource(application), token, address)
	if err != nil {
		return 1, err
	}
	url := "http://" + address + "/?token=" + token
	if err := application.writeUIListening(invocation.JSON, address, url); err != nil {
		return 1, err
	}
	if invocation.Open {
		open := application.uiOpen
		if open == nil {
			open = dashboard.OpenBrowser
		}
		if err := open(url); err != nil {
			_, _ = fmt.Fprintf(application.stderr, "ruk: could not open a browser: %v\n", err)
		}
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := dashboard.Serve(ctx, listener, handler); err != nil {
		return 1, err
	}
	return 0, nil
}

func (application *Application) writeUIListening(jsonMode bool, address, url string) error {
	if jsonMode {
		encoded, err := json.Marshal(uiListening{Status: "listening", Address: address, URL: url})
		if err != nil {
			return fmt.Errorf("encode dashboard address: %w", err)
		}
		_, err = fmt.Fprintln(application.stdout, string(encoded))
		return err
	}
	_, err := fmt.Fprintf(application.stdout, "Ruk dashboard: %s\nPress Ctrl+C to stop.\n", url)
	return err
}

// parseUIPort takes the last --port value, matching last-scalar-wins for
// other options. Zero or no value picks a free port.
func parseUIPort(values []string) (int, error) {
	if len(values) == 0 {
		return 0, nil
	}
	value := values[len(values)-1]
	port, err := strconv.Atoi(value)
	if err != nil || port < 0 || port > 65535 {
		return 0, fmt.Errorf("ui --port must be a whole number from 0 to 65535, got %q", value)
	}
	return port, nil
}
