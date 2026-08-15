//go:build !windows

package ports

import (
	"fmt"
	"os"
	"syscall"
)

func verifyRegistryRootOwner(info os.FileInfo, _ string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("directory must be owned by the current user and mode 0700")
	}
	return nil
}

func verifyRegistryFileOwner(info os.FileInfo, _ string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("file must be owned by the current user and mode 0600")
	}
	return nil
}

func verifyRegistryRootPathComponent(info os.FileInfo, _ string) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path component is not a directory")
	}
	return nil
}
