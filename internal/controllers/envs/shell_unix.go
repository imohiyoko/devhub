//go:build !windows

package envs

import "os/exec"

// shellCmd builds the sh(1) invocation for runShell. sh is POSIX-required and
// available on every supported OS, making it the safest choice for background
// process launching regardless of the user's interactive shell (bash/zsh/fish/…).
func shellCmd(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command) //execaudit:envs-run-shell
}
