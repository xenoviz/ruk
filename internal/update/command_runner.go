package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func OSCommandRunner(ctx context.Context, command string, args []string) (CommandResult, error) {
	return OSCommandRunnerWithIO(ctx, command, args, CommandIO{MachineReadable: true})
}

type commandSpec struct {
	command string
	args    []string
	cmdLine string
}

// commandSpecForPlatform describes the process launch without mutating an
// exec.Cmd. Windows package managers are commonly installed as .cmd/.bat
// shims; CreateProcess cannot launch those files directly, so the Windows
// configure layer applies this spec through SysProcAttr.CmdLine.
func commandSpecForPlatform(goos, comspec, command string, args []string) (commandSpec, error) {
	if goos != "windows" || !isWindowsPackageShim(command) {
		return commandSpec{command: command, args: append([]string(nil), args...)}, nil
	}
	if strings.TrimSpace(comspec) == "" {
		comspec = "cmd.exe"
	}
	parts := make([]string, 0, len(args)+1)
	for _, value := range append([]string{command}, args...) {
		quoted, err := quoteWindowsCmdToken(value)
		if err != nil {
			return commandSpec{}, err
		}
		parts = append(parts, quoted)
	}
	// CALL preserves the shim's exit code while allowing cmd.exe to resolve
	// bare npm/pnpm/yarn names through PATHEXT.
	commandText := "call " + strings.Join(parts, " ")
	quotedComspec, err := quoteWindowsCmdToken(comspec)
	if err != nil {
		return commandSpec{}, err
	}
	return commandSpec{
		command: comspec,
		args:    []string{comspec, "/d", "/s", "/c", commandText},
		cmdLine: quotedComspec + " /d /s /c \"" + commandText + "\"",
	}, nil
}

func isWindowsPackageShim(command string) bool {
	base := command
	if index := strings.LastIndexAny(base, `/\`); index >= 0 {
		base = base[index+1:]
	}
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
		return true
	}
	switch lower {
	case "npm", "npx", "pnpm", "yarn":
		return true
	default:
		return false
	}
}

func quoteWindowsCmdToken(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n\"%^!") {
		return "", fmt.Errorf("unsafe Windows command token")
	}
	return `"` + value + `"`, nil
}

// OSCommandRunnerWithIO runs a command with bounded diagnostic tails. Human
// mode tees output to the supplied streams and forwards stdin; machine mode
// keeps all streams private so structured output remains uncontaminated.
func OSCommandRunnerWithIO(ctx context.Context, command string, args []string, commandIO CommandIO) (CommandResult, error) {
	process := exec.CommandContext(ctx, command, args...)
	if pid, ok := ctx.Value(packageUpdatePIDKey{}).(int); ok && pid > 0 {
		environment := make([]string, 0, len(os.Environ())+1)
		for _, entry := range os.Environ() {
			if strings.HasPrefix(entry, "RUK_UPDATE_PID=") {
				continue
			}
			environment = append(environment, entry)
		}
		process.Env = append(environment, "RUK_UPDATE_PID="+strconv.Itoa(pid))
	}
	var err error
	if err = configureUpdateCommand(process); err != nil {
		return CommandResult{}, err
	}
	stdout, stderr := newTailBuffer(MaxCommandTail), newTailBuffer(MaxCommandTail)
	if commandIO.MachineReadable {
		process.Stdin = nil
		process.Stdout, process.Stderr = stdout, stderr
	} else {
		process.Stdin = commandIO.Stdin
		if commandIO.Stdout != nil {
			process.Stdout = io.MultiWriter(stdout, commandIO.Stdout)
		} else {
			process.Stdout = stdout
		}
		if commandIO.Stderr != nil {
			process.Stderr = io.MultiWriter(stderr, commandIO.Stderr)
		} else {
			process.Stderr = stderr
		}
	}
	err = process.Run()
	if err == nil {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitErr.ExitCode()}, nil
	}
	return CommandResult{}, err
}

// tailBuffer retains only the most recent bytes from a command. Package
// managers and custom wrappers can be arbitrarily noisy; updater diagnostics
// must stay useful without letting suppressed output grow memory without bound.
type tailBuffer struct {
	bytes []byte
	limit int
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (buffer *tailBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if buffer.limit <= 0 {
		return written, nil
	}
	if len(value) >= buffer.limit {
		buffer.bytes = append(buffer.bytes[:0], value[len(value)-buffer.limit:]...)
		return written, nil
	}
	overflow := len(buffer.bytes) + len(value) - buffer.limit
	if overflow > 0 {
		copy(buffer.bytes, buffer.bytes[overflow:])
		buffer.bytes = buffer.bytes[:len(buffer.bytes)-overflow]
	}
	buffer.bytes = append(buffer.bytes, value...)
	return written, nil
}

func (buffer *tailBuffer) String() string { return string(buffer.bytes) }
