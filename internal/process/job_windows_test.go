//go:build windows

package process

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"unsafe"
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
		queryActive: func(syscall.Handle) (uint32, error) {
			calls = append(calls, "query")
			return 0, nil
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
		queryActive:      func(syscall.Handle) (uint32, error) { return 0, nil },
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

func TestWindowsJobWaitEmptyPollsActiveProcessesAndKeepsJobOpen(t *testing.T) {
	var queries int
	var calls []string
	system := windowsJobSystem{
		create:           func() (syscall.Handle, error) { return 10, nil },
		setLimits:        func(syscall.Handle) error { return nil },
		openProcess:      func(uint32, uint32) (syscall.Handle, error) { return 20, nil },
		assign:           func(syscall.Handle, syscall.Handle) error { return nil },
		terminate:        func(syscall.Handle) error { return nil },
		terminateProcess: func(syscall.Handle) error { return nil },
		queryActive: func(syscall.Handle) (uint32, error) {
			queries++
			calls = append(calls, "query")
			if queries == 1 {
				return 1, nil
			}
			return 0, nil
		},
		close: func(syscall.Handle) error { calls = append(calls, "close"); return nil },
	}
	job, err := newWindowsJobWith(system)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.WaitEmpty(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatalf("queries = %d, want two active-process queries", queries)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"query", "query", "close"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want query polling before explicit close %#v", calls, want)
	}
}

func TestWindowsJobWaitEmptyTerminatesOnceOnCancellationAndKeepsPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var terminateCalls, queries int
	system := windowsJobSystem{
		create:           func() (syscall.Handle, error) { return 10, nil },
		setLimits:        func(syscall.Handle) error { return nil },
		openProcess:      func(uint32, uint32) (syscall.Handle, error) { return 20, nil },
		assign:           func(syscall.Handle, syscall.Handle) error { return nil },
		terminate:        func(syscall.Handle) error { terminateCalls++; return nil },
		terminateProcess: func(syscall.Handle) error { return nil },
		queryActive: func(syscall.Handle) (uint32, error) {
			queries++
			if queries == 1 {
				return 1, nil
			}
			return 0, nil
		},
		close: func(syscall.Handle) error { return nil },
	}
	job, err := newWindowsJobWith(system)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.WaitEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	if terminateCalls != 1 || queries != 2 {
		t.Fatalf("terminate calls = %d, queries = %d; want one termination and continued polling", terminateCalls, queries)
	}
}

func TestWindowsJobWaitEmptyReportsActiveProcessQueryError(t *testing.T) {
	queryErr := errors.New("query failed")
	var terminateCalls int
	system := windowsJobSystem{
		create:           func() (syscall.Handle, error) { return 10, nil },
		setLimits:        func(syscall.Handle) error { return nil },
		openProcess:      func(uint32, uint32) (syscall.Handle, error) { return 20, nil },
		assign:           func(syscall.Handle, syscall.Handle) error { return nil },
		terminate:        func(syscall.Handle) error { terminateCalls++; return nil },
		terminateProcess: func(syscall.Handle) error { return nil },
		queryActive:      func(syscall.Handle) (uint32, error) { return 0, queryErr },
		close:            func(syscall.Handle) error { return nil },
	}
	job, err := newWindowsJobWith(system)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.WaitEmpty(context.Background()); !errors.Is(err, queryErr) {
		t.Fatalf("error = %v, want wrapped query error", err)
	}
	if terminateCalls != 0 {
		t.Fatalf("terminate calls = %d, want no termination without cancellation", terminateCalls)
	}
}

func TestWindowsJobCloseFailureRemainsRetryable(t *testing.T) {
	closeCalls := 0
	system := windowsJobSystem{
		create:           func() (syscall.Handle, error) { return 10, nil },
		setLimits:        func(syscall.Handle) error { return nil },
		openProcess:      func(uint32, uint32) (syscall.Handle, error) { return 20, nil },
		assign:           func(syscall.Handle, syscall.Handle) error { return nil },
		terminate:        func(syscall.Handle) error { return nil },
		terminateProcess: func(syscall.Handle) error { return nil },
		queryActive:      func(syscall.Handle) (uint32, error) { return 0, nil },
		close: func(syscall.Handle) error {
			closeCalls++
			if closeCalls == 1 {
				return errors.New("close failed")
			}
			return nil
		},
	}
	job, err := newWindowsJobWith(system)
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err == nil {
		t.Fatal("first close unexpectedly succeeded")
	}
	if err := job.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("repeated close failed: %v", err)
	}
	if closeCalls != 2 {
		t.Fatalf("close calls = %d, want retry after failure and no call after success", closeCalls)
	}
}

func TestWindowsJobBasicAccountingInformationLayout(t *testing.T) {
	var accounting windowsJobBasicAccountingInformation
	if got, want := unsafe.Sizeof(accounting), uintptr(48); got != want {
		t.Fatalf("accounting size = %d, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(accounting.ActiveProcesses), uintptr(40); got != want {
		t.Fatalf("ActiveProcesses offset = %d, want %d", got, want)
	}
}

func TestWindowsCallErrorNormalizesEmptyErrno(t *testing.T) {
	if !errors.Is(windowsCallError(syscall.Errno(0)), syscall.EINVAL) {
		t.Fatal("zero Windows errno was not normalized")
	}
}
