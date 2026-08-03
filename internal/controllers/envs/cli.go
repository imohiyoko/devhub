package envs

// This file is the exported surface the devhub CLI (cmd/devhub) consumes. The
// CLI opens the shared SQLite store directly (WAL + busy_timeout make a second
// process safe) instead of going through HTTP: the API token lives only in
// the server's memory by design, and persisting it for a CLI would weaken
// that posture. The only registry write the CLI performs is the launch record
// appended by StartEnvironment (launch.go) — safe cross-process because
// AppendLaunch is a single-row INSERT; bulk rewrites (SaveLaunches) stay
// inside the server process.

// EnvStatus summarizes one environment for the CLI list view.
type EnvStatus struct {
	ID        string
	Name      string
	Processes int
	LivePorts []int
}

// StopOutcome describes one live target port and what happened to it.
type StopOutcome struct {
	Port    int
	PID     int
	Avoided bool  // excluded from killing (e.g. devhub's own port)
	Err     error // kill failure (protected port, taskkill error); nil = killed
}

// StopEnvironment kills the live listeners on envID's target ports. Ports in
// avoidPorts are reported (Avoided) but never killed — the CLI passes devhub's
// own port, since e.g. the devhub-verify example env declares base port 8765
// and a naive kill would take down the main instance instead of the offset
// one. Per-port failures (protected ports, kill errors) land in the outcome's
// Err rather than aborting, so one refusal doesn't leave the rest running. The
// launch registry is not touched: records stay visible as 停止 in the UI,
// matching what the UI's own kill buttons do.
func (c *Controller) StopEnvironment(envID string, avoidPorts ...int) ([]StopOutcome, error) {
	env, err := c.findEnv(envID)
	if err != nil {
		return nil, err
	}
	data, err := c.store.LoadLaunches()
	if err != nil {
		return nil, err
	}
	avoid := map[int]bool{}
	for _, p := range avoidPorts {
		avoid[p] = true
	}
	live := c.livePortIndex()
	var out []StopOutcome
	for _, port := range stopTargetPorts(env, toAnySlice(data["launches"])) {
		pid, ok := live[port]
		if !ok {
			continue
		}
		o := StopOutcome{Port: port, PID: pid}
		if avoid[port] {
			o.Avoided = true
		} else {
			o.Err = c.ports.KillPortProcess(port, pid)
		}
		out = append(out, o)
	}
	return out, nil
}

// EnvStatuses returns every defined environment with the subset of its target
// ports that currently have a listener, in definition order.
func (c *Controller) EnvStatuses() ([]EnvStatus, error) {
	envsDoc, err := c.store.LoadEnvs()
	if err != nil {
		return nil, err
	}
	launchDoc, err := c.store.LoadLaunches()
	if err != nil {
		return nil, err
	}
	launches := toAnySlice(launchDoc["launches"])
	live := c.livePortIndex()
	var out []EnvStatus
	for _, env := range decodeEnvironments(envsDoc) {
		st := EnvStatus{ID: env.ID, Name: env.Name, Processes: len(env.Processes)}
		for _, port := range stopTargetPorts(env, launches) {
			if _, ok := live[port]; ok {
				st.LivePorts = append(st.LivePorts, port)
			}
		}
		out = append(out, st)
	}
	return out, nil
}
