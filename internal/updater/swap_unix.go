//go:build !windows

package updater

import "os"

// swapBinary replaces exePath with newPath. On Unix a running executable can be
// renamed over: the running process keeps its open file (inode), while the path
// now resolves to the new binary — so the following re-exec launches the new
// image. newPath and exePath live in the same directory, so this rename is
// atomic within one filesystem.
func swapBinary(newPath, exePath string) error {
	return os.Rename(newPath, exePath)
}

// CleanupOld is a no-op on Unix: the swap leaves no leftover file. It exists so
// startup code can call it unconditionally.
func CleanupOld() {}
