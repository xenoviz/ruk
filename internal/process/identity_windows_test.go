//go:build windows

package process

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/xenoviz/ruk/internal/state"
)

func TestWindowsInspectorExitedProcessWithRetainedHandle(t *testing.T) {
	for _, exitCode := range []int{0, 259} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, executable, "-test.run=^TestWindowsInspectorChildProcess$")
			command.Env = append(os.Environ(), "RUK_INSPECTOR_TEST_EXIT="+strconv.Itoa(exitCode))
			command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel()
				if command.ProcessState == nil {
					_ = command.Wait()
				}
			}()
			pid := command.Process.Pid
			// An independent handle keeps the process object openable after exit.
			handle, err := syscall.OpenProcess(uint32(processQueryLimitedInformation)|syscall.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.CloseHandle(handle)
			inspector := Inspector{}
			live, err := inspector.Inspect(ctx, pid)
			if err != nil || !live.Alive || !live.IdentityKnown || live.Identity == "" {
				t.Fatalf("live child = %#v, error = %v", live, err)
			}
			if err := input.Close(); err != nil {
				t.Fatal(err)
			}
			waitErr := command.Wait()
			if command.ProcessState == nil || command.ProcessState.ExitCode() != exitCode {
				t.Fatalf("child exit = %v, error = %v", command.ProcessState, waitErr)
			}
			wait, err := syscall.WaitForSingleObject(handle, 0)
			if err != nil || wait != syscall.WAIT_OBJECT_0 {
				t.Fatalf("child handle wait = %d, error = %v", wait, err)
			}
			exited, err := inspector.Inspect(ctx, pid)
			if err != nil || exited.Alive || exited.IdentityKnown || exited.Identity != "" {
				t.Fatalf("exited child with retained handle = %#v, error = %v", exited, err)
			}
			record := state.TrackedProcessRecord{PID: int64(pid), StartedAt: live.Identity}
			if err := (Runner{Cleaner: NativeProcessCleaner{Probe: inspector}}).verifyDetachedTree(ctx, record, RunOptions{Mode: Detached}); err != nil {
				t.Fatalf("completed child falsely retained by supervisor: %v", err)
			}
			// A dead leader must still allow discovery of surviving descendants.
			tracker := Tracker{Probe: inspector, DescendantsExist: func(context.Context, int) (bool, error) { return true, nil }}
			var unavailable *IdentityUnavailableError
			if _, err := tracker.Exists(ctx, record); !errors.As(err, &unavailable) {
				t.Fatalf("surviving descendant tree error = %v, want identity safety refusal", err)
			}
		})
	}
}

func TestWindowsInspectorChildProcess(t *testing.T) {
	value, helper := os.LookupEnv("RUK_INSPECTOR_TEST_EXIT")
	if !helper {
		return
	}
	exitCode, err := strconv.Atoi(value)
	if err != nil {
		os.Exit(2)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		os.Exit(3)
	}
	os.Exit(exitCode)
}
