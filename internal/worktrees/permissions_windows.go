//go:build windows

package worktrees

import "os"

// verifyIndexRootOwner is a stub on Windows. Unlike port reservations, this
// index is display-only discovery metadata — it never authorizes a mutation,
// and every per-repo registry it points at is independently validated on read.
// Reparse points are already rejected by the Lstat symlink/irregular-file
// check before this function runs.
func verifyIndexRootOwner(os.FileInfo, string) error { return nil }

// verifyIndexFileOwner is a stub on Windows. Unlike port reservations, this
// index is display-only discovery metadata — it never authorizes a mutation,
// and every per-repo registry it points at is independently validated on read.
// Reparse points are already rejected by the Lstat symlink/irregular-file
// check in Read.
func verifyIndexFileOwner(os.FileInfo, string) error { return nil }
