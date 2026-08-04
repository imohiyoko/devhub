//go:build windows

package envs

import (
	"os/exec"
	"strings"
)

// spawnedCommandLine returns the command line a spawned command carries. See
// the unix build for why this is platform-split: runShell hands Windows the
// raw command line through SysProcAttr.CmdLine, so Args alone would only ever
// say "cmd".
func spawnedCommandLine(cmd *exec.Cmd) string {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "" {
		return cmd.SysProcAttr.CmdLine
	}
	return strings.Join(cmd.Args, " ")
}
