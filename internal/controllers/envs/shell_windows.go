//go:build windows

package envs

import (
	"os/exec"
	"syscall"
)

// shellCmd builds the cmd.exe invocation for runShell. Go's default Windows
// argument encoding escapes inner double quotes as \" (the MSVCRT convention),
// but cmd.exe does not interpret \" — a command containing quotes would reach
// cmd.exe with its quoting structure destroyed and the child would die at
// startup (issue #114). Passing the raw command line via SysProcAttr.CmdLine
// bypasses that encoding; in the `cmd /S /C "..."` form, /S makes cmd strip
// exactly the first and last quote and run everything in between verbatim.
func shellCmd(command string) *exec.Cmd {
	cmd := exec.Command("cmd") //execaudit:envs-run-shell
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /S /C "` + command + `"`}
	return cmd
}
