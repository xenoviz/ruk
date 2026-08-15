//go:build windows

package process

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestWindowsBatchCommandLineTable(t *testing.T) {
	tests := []struct {
		name    string
		comspec string
		batch   string
		args    []string
		want    string
	}{
		{name: "simple", comspec: `C:\Windows\System32\cmd.exe`, batch: `C:\tools\build.cmd`, want: `C:\Windows\System32\cmd.exe /d /s /c "C:\tools\build.cmd"`},
		{name: "spaces", comspec: `C:\Windows\System32\cmd.exe`, batch: `C:\Program Files\Ruk\run.bat`, args: []string{"a b"}, want: `C:\Windows\System32\cmd.exe /d /s /c ""C:\Program Files\Ruk\run.bat" "a b""`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := windowsBatchCommandLine(test.comspec, test.batch, test.args); got != test.want {
				t.Fatalf("command line = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWindowsEnvironmentValueIsCaseInsensitive(t *testing.T) {
	environment := []string{"Path=C:\\bin", "cOmSpEc=C:\\Windows\\System32\\cmd.exe"}
	if got := windowsEnvironmentValue(environment, "COMSPEC"); got != `C:\Windows\System32\cmd.exe` {
		t.Fatalf("COMSPEC = %q", got)
	}
	if got := windowsEnvironmentValue(environment, "missing"); got != "" {
		t.Fatalf("missing variable = %q", got)
	}
}

func TestWindowsJobAssignmentAndCloseOrdering(t *testing.T) {
	var calls []string
	system := windowsJobSystem{
		create:           func() (syscall.Handle, error) { calls = append(calls, "create"); return 10, nil },
		setLimits:        func(syscall.Handle) error { calls = append(calls, "limits"); return nil },
		openProcess:      func(uint32, uint32) (syscall.Handle, error) { calls = append(calls, "open"); return 20, nil },
		assign:           func(syscall.Handle, syscall.Handle) error { calls = append(calls, "assign"); return nil },
		terminate:        func(syscall.Handle) error { calls = append(calls, "terminate"); return nil },
		terminateProcess: func(syscall.Handle) error { calls = append(calls, "terminate-process"); return nil },
		wait: func(syscall.Handle, uint32) (uint32, error) {
			calls = append(calls, "wait")
			return waitObjectSignaled, nil
		},
		close: func(syscall.Handle) error { calls = append(calls, "close"); return nil },
	}
	job, err := newWindowsJobWith(system)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.AssignProcess(42); err != nil {
		t.Fatal(err)
	}
	if err := job.Terminate(); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "limits", "open", "assign", "close", "terminate", "close"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestWindowsJobRejectsIncompleteBoundary(t *testing.T) {
	if _, err := newWindowsJobWith(windowsJobSystem{}); err == nil || !strings.Contains(err.Error(), "boundary") {
		t.Fatalf("error = %v, want incomplete boundary error", err)
	}
}

func TestWindowsJobAssignmentFailureClosesOpenedProcessHandle(t *testing.T) {
	var calls []string
	system := windowsJobSystem{
		create:      func() (syscall.Handle, error) { return 10, nil },
		setLimits:   func(syscall.Handle) error { return nil },
		openProcess: func(uint32, uint32) (syscall.Handle, error) { calls = append(calls, "open"); return 20, nil },
		assign: func(syscall.Handle, syscall.Handle) error {
			calls = append(calls, "assign")
			return errors.New("assignment refused")
		},
		terminate:        func(syscall.Handle) error { return nil },
		terminateProcess: func(syscall.Handle) error { calls = append(calls, "terminate-process"); return nil },
		wait:             func(syscall.Handle, uint32) (uint32, error) { return waitObjectSignaled, nil },
		close:            func(syscall.Handle) error { calls = append(calls, "close"); return nil },
	}
	job, err := newWindowsJobWith(system)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.AssignProcess(42); err == nil {
		t.Fatal("AssignProcess succeeded after injected assignment failure")
	}
	if want := []string{"open", "assign", "close"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestWindowsJobWaitEmptyKeepsJobOpenForDescendants(t *testing.T) {
	var waits int
	var closed bool
	system := windowsJobSystem{
		create:           func() (syscall.Handle, error) { return 10, nil },
		setLimits:        func(syscall.Handle) error { return nil },
		openProcess:      func(uint32, uint32) (syscall.Handle, error) { return 20, nil },
		assign:           func(syscall.Handle, syscall.Handle) error { return nil },
		terminate:        func(syscall.Handle) error { return nil },
		terminateProcess: func(syscall.Handle) error { return nil },
		wait: func(syscall.Handle, uint32) (uint32, error) {
			waits++
			if waits == 1 {
				return waitObjectTimeout, nil
			}
			return waitObjectSignaled, nil
		},
		close: func(syscall.Handle) error { closed = true; return nil },
	}
	job, err := newWindowsJobWith(system)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.WaitEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waits != 2 || closed {
		t.Fatalf("waits = %d, closed = %v; job must remain open until caller closes it", waits, closed)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("job close was not observed")
	}
}

func TestWindowsCallErrorNormalizesEmptyErrno(t *testing.T) {
	if !errors.Is(windowsCallError(syscall.Errno(0)), syscall.EINVAL) {
		t.Fatal("zero Windows errno was not normalized")
	}
}
