package dashboard

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser asks the operating system to open url in the default browser.
// It starts the opener and returns without waiting for the browser.
func OpenBrowser(url string) error {
	command, err := browserCommand(runtime.GOOS, url)
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	go func() { _ = command.Wait() }()
	return nil
}

func browserCommand(goos, url string) (*exec.Cmd, error) {
	switch goos {
	case "darwin":
		return exec.Command("open", url), nil
	case "windows":
		// rundll32 receives the URL as one argument, so cmd.exe never parses
		// the token's query string.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url), nil
	case "linux", "freebsd", "openbsd", "netbsd":
		return exec.Command("xdg-open", url), nil
	default:
		return nil, fmt.Errorf("opening a browser is not supported on %s", goos)
	}
}
