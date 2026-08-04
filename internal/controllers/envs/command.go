package envs

// The seam for the external commands the runtime adapters run. Every such
// spawn funnels through execRunner, so the execaudit registry has one call site
// to point at (mirroring how the git controller funnels through its runCmd),
// and tests substitute a fake runner instead of requiring Docker.

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// commandRunner runs one external command to completion and returns what it
// wrote. Implementations never build a shell string (plan §6.6): the argv is
// passed through as given. This is deliberately separate from the terminal
// spawn path, which exists to run the user's own command line.
type commandRunner interface {
	Run(ctx context.Context, cwd, name string, args ...string) (stdout, stderr string, err error)
}

// execRunner is the production runner: it runs the command in cwd, bounded by
// ctx, and captures both streams. A non-zero exit is returned as err with
// stderr intact, so callers can surface the tool's own message.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //execaudit:envs-runtime
	if cwd != "" {
		cmd.Dir = cwd
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// cliError turns a failed invocation into the reason a user sees: the tool's
// own first stderr line when it wrote one, the exec error otherwise (a missing
// working directory or a timeout writes nothing to stderr). Shared by the
// runtime adapters so Docker's and Colima's own wording reaches the UI
// unparaphrased — devhub cannot diagnose their failures better than they can.
func cliError(stderr string, err error) error {
	if line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n"); line != "" {
		return errors.New(line)
	}
	return err
}
