//go:build windows

package ports

import "os"

// Windows ACLs are authoritative; os.FileMode does not expose the effective
// owner-only ACL. The native lock/file adapters are still created with the
// requested restrictive mode, and callers should retain the platform ACL
// caveat in security documentation.
func verifyRegistryRootOwner(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return os.ErrPermission
	}
	return nil
}

func verifyRegistryFileOwner(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return os.ErrPermission
	}
	return nil
}
