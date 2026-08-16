//go:build !windows

package process

import (
	"os"
	"testing"
)

func TestForegroundTerminalForPipeDoesNotTransferOwnership(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	terminal, err := foregroundTerminalFor(reader, true)
	if err != nil {
		t.Fatal(err)
	}
	if terminal != nil {
		t.Fatal("pipe was treated as a controlling terminal")
	}
}

func TestForegroundTerminalRestoreFailsClosedForInvalidDescriptor(t *testing.T) {
	terminal := &foregroundTerminal{fd: -1, previous: 1}
	if err := terminal.restore(); err == nil {
		t.Fatal("invalid terminal descriptor restored successfully")
	}
}
