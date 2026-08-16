//go:build !windows

package update

import "os/exec"

// configureUpdateCommand keeps non-Windows execution on os/exec's direct
// argv path. Windows command-line quoting must not leak into POSIX launchers.
func configureUpdateCommand(_ *exec.Cmd) error { return nil }
