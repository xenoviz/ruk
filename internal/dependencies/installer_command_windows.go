//go:build windows

package dependencies

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// configureInstallerCommand routes Windows batch shims through cmd.exe. Go's
// CreateProcess cannot launch .cmd/.bat files directly, and the extra quote
// pair around the /c command protects paths containing spaces.
func configureInstallerCommand(command *exec.Cmd) error {
	if command == nil || !isWindowsBatchFile(command.Path) {
		return nil
	}
	comspec := windowsComSpec(command.Env)
	if comspec == "" {
		return errors.New("dependency installer: COMSPEC is unavailable for batch command")
	}
	original := append([]string(nil), command.Args...)
	if len(original) == 0 {
		original = []string{command.Path}
	}
	batch := original[0]
	if batch == "" {
		batch = command.Path
	}
	args := append([]string(nil), original[1:]...)
	command.Path = comspec
	command.Args = append([]string{comspec, "/d", "/s", "/c", batch}, args...)
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: windowsBatchCommandLine(comspec, batch, args)}
	return nil
}

func isWindowsBatchFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cmd" || ext == ".bat"
}

func windowsComSpec(environment []string) string {
	if value := windowsEnvironmentValue(environment, "COMSPEC"); value != "" {
		return value
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

func windowsEnvironmentValue(environment []string, name string) string {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func windowsBatchCommandLine(comspec, batch string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, syscall.EscapeArg(batch))
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return syscall.EscapeArg(comspec) + " /d /s /c \"" + strings.Join(parts, " ") + "\""
}
