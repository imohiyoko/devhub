//go:build !windows

package server

import (
	"log"
	"os"
	"syscall"
)

// reexec replaces the current process image with a fresh copy of the binary.
// The listening socket carries close-on-exec, so the port is released before the
// new image binds it (Run retries the bind briefly to absorb any TIME_WAIT).
func reexec(token string) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("reexec: failed to resolve executable: %v", err)
		return
	}
	env := append(os.Environ(), "DEVHUB_API_TOKEN="+token)
	// syscall.Exec only returns on failure; if it does, the old process keeps
	// running, so surface the reason instead of swallowing it.
	if err := syscall.Exec(exe, os.Args, env); err != nil {
		log.Printf("reexec: syscall.Exec failed: %v", err)
	}
}
