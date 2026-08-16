//go:build !windows

package dependencies

import "os/exec"

func configureInstallerCommand(_ *exec.Cmd) error { return nil }
