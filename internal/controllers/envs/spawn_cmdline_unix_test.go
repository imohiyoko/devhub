//go:build !windows

package envs

import (
	"os/exec"
	"strings"
)

// spawnedCommandLine returns the command line a spawned command carries, so
// tests can match on it whatever the platform. On unix that is simply the
// argv; the Windows build reads SysProcAttr.CmdLine instead, because runShell
// passes the raw command line there and leaves Args as just "cmd"
// (see shell_windows.go).
func spawnedCommandLine(cmd *exec.Cmd) string { return strings.Join(cmd.Args, " ") }
