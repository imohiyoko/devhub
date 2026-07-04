package git

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// gitEnvHardened returns env entries that stop git from hanging on credential
// prompts (so a missing credential fails fast instead of blocking).
func gitEnvHardened() []string {
	return []string{"GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes"}
}

// gitEnvHardenedC additionally pins git's messages to English (LC_ALL/LANG=C) so
// stderr matching (e.g. "already exists") stays reliable on non-English locales.
func gitEnvHardenedC() []string {
	return append(gitEnvHardened(), "LC_ALL=C", "LANG=C")
}

// runCmd executes name+args in cwd. A positive timeout bounds the run; extraEnv
// is appended to the inherited environment. Returns stdout, stderr, whether it
// timed out, and the run error (non-nil for a non-zero exit, like check=True).
func runCmd(cwd string, timeout time.Duration, extraEnv []string, name string, args ...string) (stdout, stderr string, timedOut bool, err error) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...) //execaudit:git
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), errb.String(), true, err
	}
	return out.String(), errb.String(), false, err
}
