package envs

// The side-effecting half of launching: killing listeners and opening
// terminals, consuming what planner.go computed. The terminal machinery itself
// (per-OS emulator handling) stays in terminal.go; this file is the seam
// between a plan and its execution.

import "time"

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

// runSpawns opens a terminal per planned step, pausing each step's delay
// before the next. The HTTP path runs the loop on a goroutine (the request
// returns while terminals spawn); the CLI path runs it inline — a short-lived
// `devhub env start` process must not exit while launches are still pending on
// a goroutine, or they are simply lost.
func (c *Controller) runSpawns(steps []spawnStep, async bool) {
	run := func() {
		for i, s := range steps {
			c.openInTerminal(s.cwd, s.command, s.env)
			if i < len(steps)-1 {
				time.Sleep(s.delay)
			}
		}
	}
	if async {
		go run()
	} else {
		run()
	}
}
