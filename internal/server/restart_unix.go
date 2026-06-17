//go:build !windows

package server

import (
	"os"
	"syscall"
)

// reexec replaces the current process image with a fresh copy of the binary.
// The listening socket carries close-on-exec, so the port is released before the
// new image binds it (Run retries the bind briefly to absorb any TIME_WAIT).
func reexec(token string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	env := append(os.Environ(), "DEVHUB_API_TOKEN="+token)
	_ = syscall.Exec(exe, os.Args, env)
}
