//go:build windows

package server

import "syscall"

// detachAttr is a no-op on Windows. The unix failure it guards against — a pty
// SIGHUP when a terminal-launched `go run` parent exits — does not apply to the
// Windows console launcher, which already spawns the replacement detached enough
// to survive the old process exiting.
func detachAttr() *syscall.SysProcAttr {
	return nil
}
