//go:build windows

package updater

import (
	"os"
	"path/filepath"
)

// swapBinary replaces exePath with newPath. Windows refuses to delete or
// overwrite a running .exe, but it DOES allow renaming it — so move the running
// binary aside to "<exe>.old", then move the new one into place. The old image
// is still mapped by this process and cannot be deleted yet; the next launch
// removes it via CleanupOld. A leftover ".old" from a prior update is cleared
// first (that process has since exited, so it is now deletable).
func swapBinary(newPath, exePath string) error {
	old := exePath + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exePath, old); err != nil {
		return err
	}
	if err := os.Rename(newPath, exePath); err != nil {
		// Roll back so the current binary keeps working if the second move fails.
		_ = os.Rename(old, exePath)
		return err
	}
	return nil
}

// CleanupOld removes the "<exe>.old" left by a previous self-update, if any.
// Called once at startup, by which point the process that held it open has
// exited so the file is deletable. Best-effort: a still-locked or absent file
// is ignored.
func CleanupOld() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	_ = os.Remove(exe + ".old")
}
