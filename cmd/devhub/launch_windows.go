//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

// execLaunch runs the target command as a child process and exits with its
// status. Windows has no exec(2), so the current process stays alive as a thin
// parent, inheriting this console: the child's banner, output, and Ctrl+C behave
// as if it were launched directly, and its exit code is propagated. dir is the
// source checkout for the `go run` hand-off, empty for a plain binary.
//
// This exec.Command is the sole physical call site of the start-launch surface
// (the unix hand-off uses syscall.Exec, which the execaudit guard does not scan).
func execLaunch(argv []string, dir string) error {
	cmd := exec.Command(argv[0], argv[1:]...) //execaudit:start-launch
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		return err // failed to start the child (bad path, missing go, etc.)
	}
	os.Exit(0)
	return nil // unreachable
}
