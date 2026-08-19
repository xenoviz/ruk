//go:build windows

package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// configureUpdateCommand confines Windows command-line differences to this
// file. SysProcAttr.CmdLine is required because exec.Cmd's ordinary argv
// serialization does not preserve cmd.exe's /c quoting rules.
func configureUpdateCommand(command *exec.Cmd) error {
	if command == nil {
		return errors.New("update: Windows command is unavailable")
	}
	original := append([]string(nil), command.Args...)
	if len(original) == 0 {
		original = []string{command.Path}
	}
	spec, err := commandSpecForPlatform("windows", windowsUpdateComSpec(command.Env), command.Path, original[1:])
	if err != nil {
		return err
	}
	if spec.cmdLine == "" {
		return nil
	}
	command.Path = spec.command
	command.Args = spec.args
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: spec.cmdLine}
	return nil
}

func windowsUpdateComSpec(environment []string) string {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "COMSPEC") && value != "" {
			return value
		}
	}
	if value := os.Getenv("COMSPEC"); value != "" {
		return value
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "cmd.exe")
}
