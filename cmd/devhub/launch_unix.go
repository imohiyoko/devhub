//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// execLaunch replaces the current process image with the target command,
// optionally after changing into dir (the source checkout, for the `go run`
// hand-off). It returns only on failure; on success syscall.Exec never returns.
// No socket is open yet at this point — the server has not bound its port — so
// there is nothing to carry across the exec.
//
// This is deliberately not an exec.Command call site: the execaudit guard scans
// only exec.Command/CommandContext, and this hand-off is documented under the
// start-launch surface (whose exec.Command form lives in launch_windows.go),
// mirroring how the `restart` surface splits syscall.Exec (unix) from
// exec.Command (windows).
func execLaunch(argv []string, dir string) error {
	if dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("chdir %s: %w", dir, err)
		}
	}
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", argv[0], err)
	}
	return nil
}
