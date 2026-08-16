package dependencies

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const defaultDiagnosticLimit = 4096

// CommandRequest is the process-independent description of one installer
// command. Keeping this request as data makes dependency preparation easy to
// exercise without starting a package manager.
type CommandRequest struct {
	Command      string
	Args         []string
	Dir          string
	Env          []string
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	InheritStdio bool
}

// CommandResult is the bounded observable result of one installer command.
// A non-zero ExitCode is an installer rejection; the returned error is reserved for
// failures to start or communicate with the command.
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// CommandRunner is the operating-system seam for dependency preparation.
// Implementations must not mutate the request slices.
type CommandRunner func(context.Context, CommandRequest) (CommandResult, error)

// InstallResult exposes the command and bounded diagnostics from a successful
// or failed install. Diagnostics are always tails, so a package manager cannot
// make Ruk retain an unbounded output stream.
type InstallResult struct {
	Command []string
	Stdout  string
	Stderr  string
}

// DependencyPreparationError preserves the TypeScript public error prefix,
// while retaining the original cause for errors.Is/errors.As callers.
type DependencyPreparationError struct {
	Cause  error
	Stdout string
	Stderr string
}

func (err *DependencyPreparationError) Error() string {
	if err == nil || err.Cause == nil {
		return "Dependency installation failed"
	}
	return "Dependency installation failed: " + err.Cause.Error()
}

func (err *DependencyPreparationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// Installer prepares one package-manager dependency projection. Runner is
// optional; a nil runner uses OSCommandRunner. DiagnosticLimit defaults to
// the same 4 KiB tail used by the process package.
type Installer struct {
	Runner          CommandRunner
	DiagnosticLimit int
	Environment     []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	InheritStdio    bool
}

// NewInstaller constructs an installer around an injected command runner.
func NewInstaller(runner CommandRunner) Installer {
	return Installer{Runner: runner}
}

// Prepare installs dependencies in root using manager's exact command. The
// manager's Version is required for shared Bun and pnpm mode, matching the
// TypeScript preflight contract. Managed mode deliberately accepts any
// package-manager name and version.
func Prepare(ctx context.Context, root string, manager PackageManager, runner CommandRunner) (InstallResult, error) {
	return (Installer{Runner: runner}).Prepare(ctx, root, manager)
}

// Prepare installs dependencies in root using the configured command runner.
func (installer Installer) Prepare(ctx context.Context, root string, manager PackageManager) (InstallResult, error) {
	command := append([]string(nil), manager.Command...)
	if len(command) == 0 || command[0] == "" {
		return InstallResult{}, errors.New("Package manager command cannot be empty")
	}

	mode := manager.DependencyMode
	if mode == "" {
		mode = "managed"
	}
	if mode == "shared" {
		if err := AssertSharedBackendSupported(manager.Name, manager.Version); err != nil {
			return InstallResult{}, err
		}
	}

	args := append([]string(nil), command[1:]...)
	environment := installer.environment()
	if mode == "shared" {
		switch manager.Name {
		case "bun":
			setEnvironment(&environment, "BUN_INSTALL_GLOBAL_STORE", "1")
			if linker, configured := configuredBunLinker(args); configured && linker != "isolated" {
				return InstallResult{}, errors.New("Bun's global virtual store requires the isolated linker")
			} else if !configured {
				args = append(args, "--linker", "isolated")
			}
		case "pnpm":
			if configured, value := configuredPnpmGlobalStore(args); configured && value != "true" {
				return InstallResult{}, errors.New("pnpm's shared dependency backend requires the global virtual store")
			} else if !configured {
				args = append(args, "--config.enable-global-virtual-store=true")
			}
		default:
			return InstallResult{}, fmt.Errorf("The shared dependency backend does not support %s; use dependencyMode \"managed\"", manager.Name)
		}
	}

	request := CommandRequest{
		Command: command[0], Args: args, Dir: root, Env: environment,
		Stdin: installer.Stdin, Stdout: installer.Stdout, Stderr: installer.Stderr,
		InheritStdio: installer.InheritStdio,
	}
	result, err := installer.runner()(ctx, request)
	limit, limitErr := installer.limit()
	if limitErr != nil {
		return InstallResult{}, limitErr
	}
	result.Stdout = tail(result.Stdout, limit)
	result.Stderr = tail(result.Stderr, limit)
	prepared := InstallResult{
		Command: append(append([]string(nil), command[:1]...), args...),
		Stdout:  result.Stdout,
		Stderr:  result.Stderr,
	}
	if err != nil {
		return prepared, &DependencyPreparationError{Cause: err, Stdout: result.Stdout, Stderr: result.Stderr}
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		cause := fmt.Errorf("%s failed with exit code %d: %s", strings.Join(prepared.Command, " "), result.ExitCode, detail)
		return prepared, &DependencyPreparationError{Cause: cause, Stdout: result.Stdout, Stderr: result.Stderr}
	}
	return prepared, nil
}

// AssertSharedBackendSupported validates Ruk's shared backend/version matrix.
// Version accepts the command's usual text forms (for example "1.3.14" and
// "bun 1.3.14").
func AssertSharedBackendSupported(name, version string) error {
	minimum, ok := map[string][3]int{
		"bun":  {1, 3, 14},
		"pnpm": {10, 12, 1},
	}[name]
	if !ok {
		return fmt.Errorf("Ruk's shared dependency backend does not support %s", name)
	}
	current, ok := numericVersion(version)
	if !ok || compareVersion(current, minimum) < 0 {
		return fmt.Errorf("%s %d.%d.%d or newer is required for Ruk's shared dependency backend (found %s)", name, minimum[0], minimum[1], minimum[2], version)
	}
	return nil
}

// AssertSharedBackendSupported is also available under the shorter name
// used by the TypeScript implementation's callers.
func assertSharedBackendSupported(name, version string) error {
	return AssertSharedBackendSupported(name, version)
}

var versionPattern = regexp.MustCompile(`(^|[[:space:]]|v)([0-9]+)\.([0-9]+)\.([0-9]+)`)

func numericVersion(value string) ([3]int, bool) {
	match := versionPattern.FindStringSubmatch(value)
	if len(match) != 5 {
		return [3]int{}, false
	}
	major, majorErr := strconv.Atoi(match[2])
	minor, minorErr := strconv.Atoi(match[3])
	patch, patchErr := strconv.Atoi(match[4])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return [3]int{}, false
	}
	return [3]int{major, minor, patch}, true
}

func compareVersion(left, right [3]int) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func configuredBunLinker(args []string) (string, bool) {
	for index, argument := range args {
		if strings.HasPrefix(argument, "--linker=") {
			return strings.TrimPrefix(argument, "--linker="), true
		}
		if argument == "--linker" && index+1 < len(args) {
			return args[index+1], true
		}
	}
	return "", false
}

func configuredPnpmGlobalStore(args []string) (bool, string) {
	for _, argument := range args {
		for _, prefix := range []string{"--config.enable-global-virtual-store=", "--enable-global-virtual-store="} {
			if strings.HasPrefix(argument, prefix) {
				return true, strings.TrimPrefix(argument, prefix)
			}
		}
	}
	return false, ""
}

func (installer Installer) runner() CommandRunner {
	if installer.Runner != nil {
		return installer.Runner
	}
	return OSCommandRunner
}

func (installer Installer) environment() []string {
	if installer.Environment != nil {
		return append([]string(nil), installer.Environment...)
	}
	return os.Environ()
}

func setEnvironment(environment *[]string, key, value string) {
	entries := *environment
	prefix := key + "="
	updated := false
	result := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		if strings.HasPrefix(entry, prefix) {
			if !updated {
				result = append(result, prefix+value)
				updated = true
			}
			continue
		}
		result = append(result, entry)
	}
	if !updated {
		result = append(result, prefix+value)
	}
	*environment = result
}

func (installer Installer) limit() (int, error) {
	limit := installer.DiagnosticLimit
	if limit == 0 {
		return defaultDiagnosticLimit, nil
	}
	if limit < 0 {
		return 0, errors.New("dependency diagnostic limit must not be negative")
	}
	return limit, nil
}

func tail(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

// OSCommandRunner executes a package manager without a shell and captures
// bounded output. Context cancellation is returned as the cause so callers
// can still use errors.Is(err, context.Canceled/DeadlineExceeded).
func OSCommandRunner(ctx context.Context, request CommandRequest) (CommandResult, error) {
	stdout := newTailWriter(defaultDiagnosticLimit)
	stderr := newTailWriter(defaultDiagnosticLimit)
	var stdoutTarget io.Writer = stdout
	var stderrTarget io.Writer = stderr
	if request.InheritStdio {
		if request.Stdout == nil {
			request.Stdout = os.Stdout
		}
		if request.Stderr == nil {
			request.Stderr = os.Stderr
		}
		stdoutTarget = io.MultiWriter(request.Stdout, stdout)
		stderrTarget = io.MultiWriter(request.Stderr, stderr)
	}
	command := exec.CommandContext(ctx, request.Command, request.Args...)
	command.Dir = request.Dir
	command.Env = append([]string(nil), request.Env...)
	if request.InheritStdio {
		command.Stdin = request.Stdin
		if command.Stdin == nil {
			command.Stdin = os.Stdin
		}
	}
	command.Stdout = stdoutTarget
	command.Stderr = stderrTarget
	err := command.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

type tailWriter struct {
	limit int
	data  []byte
}

func newTailWriter(limit int) *tailWriter { return &tailWriter{limit: limit} }

func (writer *tailWriter) Write(data []byte) (int, error) {
	if len(data) >= writer.limit {
		writer.data = append(writer.data[:0], data[len(data)-writer.limit:]...)
		return len(data), nil
	}
	if len(writer.data)+len(data) > writer.limit {
		drop := len(writer.data) + len(data) - writer.limit
		writer.data = append(writer.data[:0], writer.data[drop:]...)
	}
	writer.data = append(writer.data, data...)
	return len(data), nil
}

func (writer *tailWriter) String() string { return string(writer.data) }
