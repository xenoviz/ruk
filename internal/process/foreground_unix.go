//go:build !windows

package process

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"unsafe"
)

// foregroundTerminal records the caller's terminal ownership so the shell's
// detached process group can temporarily become foreground and be restored
// after the child exits.
type foregroundTerminal struct {
	fd           int
	previous     int32
	lifetimeHeld bool
	once         sync.Once
	err          error
}

// foregroundLifetimeMu serializes terminal foreground ownership for the full
// child lifetime. Ruk does not support two managed interactive shells sharing
// one controlling terminal concurrently.
var foregroundLifetimeMu sync.Mutex
var foregroundRestoreMu sync.Mutex
var errNotControllingTerminal = errors.New("not a controlling terminal")

func foregroundTerminalFor(stdin io.Reader, requested bool) (*foregroundTerminal, error) {
	if !requested {
		return nil, nil
	}
	file, ok := stdin.(*os.File)
	if !ok || file == nil {
		return nil, nil
	}
	foregroundLifetimeMu.Lock()
	fd := int(file.Fd())
	previous, err := terminalForegroundGroup(fd)
	if errors.Is(err, syscall.ENOTTY) || errors.Is(err, errNotControllingTerminal) {
		foregroundLifetimeMu.Unlock()
		return nil, nil
	}
	if err != nil {
		foregroundLifetimeMu.Unlock()
		return nil, fmt.Errorf("process: inspect foreground terminal: %w", err)
	}
	return &foregroundTerminal{fd: fd, previous: previous, lifetimeHeld: true}, nil
}

func (terminal *foregroundTerminal) restore() error {
	if terminal == nil {
		return nil
	}
	terminal.once.Do(func() {
		if err := setTerminalForegroundGroup(terminal.fd, terminal.previous); err != nil {
			terminal.err = fmt.Errorf("process: restore foreground terminal: %w", err)
		}
		if terminal.lifetimeHeld {
			foregroundLifetimeMu.Unlock()
			terminal.lifetimeHeld = false
		}
	})
	return terminal.err
}

func terminalForegroundGroup(fd int) (int32, error) {
	var group int32
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&group)), 0, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	if group <= 0 {
		// Some pipe implementations report a successful ioctl with an empty
		// process group instead of ENOTTY. Treat that the same as any other
		// non-terminal stdin so --foreground-terminal remains harmless for
		// redirected and piped input.
		return 0, errNotControllingTerminal
	}
	return group, nil
}

func setTerminalForegroundGroup(fd int, group int32) error {
	// Once the child group owns the terminal, the supervisor is backgrounded.
	// TIOCSPGRP sends SIGTTOU to a background process group unless that signal
	// is blocked or ignored. Serialize this small process-global change so two
	// terminal restorations cannot reset each other's disposition midway through
	// the ioctl. Ruk does not install a custom SIGTTOU handler; an already-ignored
	// disposition is preserved.
	foregroundRestoreMu.Lock()
	defer foregroundRestoreMu.Unlock()
	wasIgnored := signal.Ignored(syscall.SIGTTOU)
	signal.Ignore(syscall.SIGTTOU)
	if !wasIgnored {
		defer signal.Reset(syscall.SIGTTOU)
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCSPGRP), uintptr(unsafe.Pointer(&group)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
