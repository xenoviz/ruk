//go:build windows

package update

import (
	"os/exec"
	"strings"
	"testing"
)

func TestConfigureUpdateCommandUsesRawCOMSPECCommandLine(t *testing.T) {
	command := exec.Command(`C:\Program Files\nodejs\npm.cmd`, "install", "--global", "a b")
	command.Env = []string{`cOmSpEc=C:\Windows\System32\cmd.exe`}
	if err := configureUpdateCommand(command); err != nil {
		t.Fatal(err)
	}
	if command.Path != `C:\Windows\System32\cmd.exe` || len(command.Args) != 5 || command.Args[1] != "/d" || command.Args[2] != "/s" || command.Args[3] != "/c" || !strings.Contains(command.Args[4], `"a b"`) {
		t.Fatalf("configured command = path=%q args=%#v", command.Path, command.Args)
	}
	if command.SysProcAttr == nil || !strings.Contains(command.SysProcAttr.CmdLine, ` /d /s /c "call `) || !strings.Contains(command.SysProcAttr.CmdLine, `"C:\Program Files\nodejs\npm.cmd"`) || !strings.Contains(command.SysProcAttr.CmdLine, `"a b"`) {
		t.Fatalf("raw command line = %#v", command.SysProcAttr)
	}
}

func TestConfigureUpdateCommandLeavesNativeExecutableDirect(t *testing.T) {
	command := exec.Command(`C:\Program Files\bun\bun.exe`, "--version")
	if err := configureUpdateCommand(command); err != nil {
		t.Fatal(err)
	}
	if command.Path != `C:\Program Files\bun\bun.exe` || len(command.Args) != 2 || command.SysProcAttr != nil {
		t.Fatalf("native command changed: path=%q args=%#v attr=%#v", command.Path, command.Args, command.SysProcAttr)
	}
}
