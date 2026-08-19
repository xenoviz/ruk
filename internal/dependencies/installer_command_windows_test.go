//go:build windows

package dependencies

import (
	"os/exec"
	"strings"
	"testing"
)

func TestConfigureInstallerCommandRoutesBatchShimThroughCOMSPEC(t *testing.T) {
	command := exec.Command(`C:\Program Files\nodejs\npm.cmd`, "install", "--flag", "a b")
	command.Env = []string{`cOmSpEc=C:\Windows\System32\cmd.exe`}
	if err := configureInstallerCommand(command); err != nil {
		t.Fatal(err)
	}
	if command.Path != `C:\Windows\System32\cmd.exe` {
		t.Fatalf("command path = %q", command.Path)
	}
	if len(command.Args) != 8 || command.Args[1] != "/d" || command.Args[2] != "/s" || command.Args[3] != "/c" || command.Args[4] != `C:\Program Files\nodejs\npm.cmd` || command.Args[5] != "install" || command.Args[6] != "--flag" || command.Args[7] != "a b" {
		t.Fatalf("command args = %#v", command.Args)
	}
	if command.SysProcAttr == nil || !strings.Contains(command.SysProcAttr.CmdLine, `"C:\Program Files\nodejs\npm.cmd"`) || !strings.Contains(command.SysProcAttr.CmdLine, `"a b"`) {
		t.Fatalf("command line = %#v", command.SysProcAttr)
	}
}

func TestConfigureInstallerCommandLeavesNativeExecutableUnchanged(t *testing.T) {
	command := exec.Command(`C:\Program Files\nodejs\node.exe`, "--version")
	if err := configureInstallerCommand(command); err != nil {
		t.Fatal(err)
	}
	if command.Path != `C:\Program Files\nodejs\node.exe` || len(command.Args) != 2 || command.SysProcAttr != nil {
		t.Fatalf("native command changed: path=%q args=%#v attr=%#v", command.Path, command.Args, command.SysProcAttr)
	}
}
