package envs

// The side-effecting half of launching: killing listeners and opening
// terminals, consuming what planner.go computed. The terminal machinery itself
// (per-OS emulator handling) stays in terminal.go; this file is the seam
// between a plan and its execution.

import (
	"fmt"
	"strings"
	"time"
)

// killPorts frees the planned baton targets (best-effort) and returns what it
// actually killed, pausing settle afterwards so the freed ports are released
// before processes start. Baton means take-over: the kills are the feature, so
// callers that face a user (the CLI) surface them instead of letting the port
// change hands silently.
func (c *Controller) killPorts(targets []BatonKill) []BatonKill {
	var killed []BatonKill
	for _, t := range targets {
		if err := c.ports.KillPortProcess(t.Port, t.PID); err == nil {
			killed = append(killed, t)
		}
	}
	if len(killed) > 0 {
		time.Sleep(c.settle)
	}
	return killed
}

// spawnFailure names a step whose terminal could not be started. It reports a
// failure to *launch*, not a failure of the launched command: once the emulator
// owns the process, devhub cannot see how it exits.
type spawnFailure struct {
	ID  string
	Err error
}

// runSpawns opens a terminal per planned step, pausing each step's delay
// before the next, and returns the steps that could not be started. A failing
// step does not abort the rest: a partial launch is reported, not silently
// truncated.
//
// The HTTP path runs the loop on a goroutine (the request returns while
// terminals spawn) and therefore cannot report anything — its failures are
// dropped exactly as they always were. The CLI path runs it inline, both
// because a short-lived `devhub env start` must not exit while launches are
// pending on a goroutine and because inline is what lets it report failures.
func (c *Controller) runSpawns(steps []spawnStep, async bool) []spawnFailure {
	run := func() []spawnFailure {
		var failures []spawnFailure
		for i, s := range steps {
			if err := c.openInTerminal(s.cwd, s.command, s.env); err != nil {
				failures = append(failures, spawnFailure{ID: s.id, Err: err})
			}
			if i < len(steps)-1 {
				time.Sleep(s.delay)
			}
		}
		return failures
	}
	if async {
		go run() //nolint:errcheck // nothing is left to report to: the request has returned
		return nil
	}
	return run()
}

// spawnErr renders launch failures as one error: partial success is the normal
// case, so the message names exactly which processes did not start.
func spawnErr(failures []spawnFailure) error {
	if len(failures) == 0 {
		return nil
	}
	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		parts = append(parts, fmt.Sprintf("'%s' (%v)", f.ID, f.Err))
	}
	return fmt.Errorf("起動できなかったプロセスがあります: %s", strings.Join(parts, ", "))
}
