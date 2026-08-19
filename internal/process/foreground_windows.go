//go:build windows

package process

import "io"

// Windows uses a Job Object as the durable process-tree boundary. ConPTY is
// intentionally not claimed or allocated by this dependency-free adapter.
type foregroundTerminal struct{}

func foregroundTerminalFor(io.Reader, bool) (*foregroundTerminal, error) { return nil, nil }

func (*foregroundTerminal) restore() error { return nil }
