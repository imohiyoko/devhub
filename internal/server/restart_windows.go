//go:build windows

package server

import (
	"log"
	"os"
	"os/exec"
)

// reexec spawns a fresh process and exits the current one. Windows has no
// exec(2); the new process retries the bind (in Run) until the old one releases
// the port on exit.
func reexec(token string) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("reexec: failed to resolve executable: %v", err)
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...) //execaudit:restart
	cmd.Env = append(os.Environ(), "DEVHUB_API_TOKEN="+token)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	// Only exit once the replacement is running; otherwise keep serving so a
	// failed spawn doesn't take the server down.
	if err := cmd.Start(); err != nil {
		log.Printf("reexec: failed to start new process: %v", err)
		return
	}
	os.Exit(0)
}
