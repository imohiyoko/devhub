//go:build windows

package server

import (
	"os"
	"os/exec"
)

// reexec spawns a fresh process and exits the current one. Windows has no
// exec(2); the new process retries the bind (in Run) until the old one releases
// the port on exit.
func reexec(token string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "DEVHUB_API_TOKEN="+token)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	_ = cmd.Start()
	os.Exit(0)
}
