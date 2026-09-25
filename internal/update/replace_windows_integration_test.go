//go:build windows

package update

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeExecutableEnv = "RUK_UPDATE_FAKE_EXECUTABLE"

var fakeVersionMarker = []byte("\nRUK_FAKE_VERSION=")

// TestMain lets copies of this test binary act as old and new Ruk
// executables for the replacement helper: each copy carries its version in
// an appended marker, so the helper's --version check sees real replacement.
func TestMain(m *testing.M) {
	if os.Getenv(fakeExecutableEnv) == "1" {
		runFakeExecutable()
		return
	}
	os.Exit(m.Run())
}

func runFakeExecutable() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	body, err := os.ReadFile(self)
	index := bytes.LastIndex(body, fakeVersionMarker)
	if err != nil || index < 0 {
		os.Exit(2)
	}
	version := strings.TrimSpace(string(body[index+len(fakeVersionMarker):]))
	switch {
	case len(os.Args) > 1 && os.Args[1] == "--version":
		fmt.Println(version)
	case len(os.Args) > 1 && os.Args[1] == "hold":
		// Keep the image locked the way a running `ruk update` does.
		time.Sleep(3 * time.Second)
	}
	os.Exit(0)
}

func writeFakeExecutable(t *testing.T, body []byte, path, version string) {
	t.Helper()
	contents := append(append(append([]byte(nil), body...), fakeVersionMarker...), []byte(version+"\n")...)
	if err := os.WriteFile(path, contents, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsReplacementHelperReplacesLockedExecutable(t *testing.T) {
	t.Setenv(fakeExecutableEnv, "1")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	// A space in the install path covers the cmd.exe /s quoting as well.
	dir := filepath.Join(t.TempDir(), "Program Files", "ruk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "ruk.exe")
	candidate := filepath.Join(dir, ".ruk.exe.ruk-0.2.0-1.new")
	writeFakeExecutable(t, body, executable, "0.1.0")
	writeFakeExecutable(t, body, candidate, "0.2.0")

	holder := exec.Command(executable, "hold")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	holderDone := make(chan error, 1)
	go func() { holderDone <- holder.Wait() }()

	helper, script, err := WindowsReplacementPlan(executable, candidate, "0.2.0", holder.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	command, err := windowsHelperCommand(helper)
	if err != nil {
		t.Fatal(err)
	}
	// Match production: no console input for the detached helper.
	command.Stdin = nil
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	helperDone := make(chan error, 1)
	go func() { helperDone <- command.Wait() }()

	select {
	case helperErr := <-helperDone:
		<-holderDone
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if helperErr != nil {
			t.Fatalf("replacement helper failed: %v; remaining files: %v", helperErr, names)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("replacement helper did not finish within 90 seconds")
	}

	output, err := exec.Command(executable, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "0.2.0" {
		t.Fatalf("executable version after replacement = %q, %v; want 0.2.0", output, err)
	}
	for _, leftover := range []string{candidate, candidate + ".backup", helper} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatalf("replacement left %s behind: %v", leftover, err)
		}
	}
}
