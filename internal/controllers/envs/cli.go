package envs

import "sort"

// This file is the exported surface the devhub CLI (cmd/devhub) consumes. The
// CLI opens the shared SQLite store directly (WAL + busy_timeout make a second
// reader process safe) instead of going through HTTP: the API token lives only
// in the server's memory by design, and persisting it for a CLI would weaken
// that posture. Everything here is therefore read-only towards the store —
// SaveLaunches rewrites the whole table under a per-process mutex, so registry
// writes must stay inside the server process.

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

// stopTargetPorts computes the deduplicated, sorted candidate ports for
// stopping envID: the declared port specs of the environment's processes plus
// the ports its launch records pin down (assigned_port when the launch got an
// offset port, the recorded spec otherwise — the same precedence the launch
// list uses for its live badge). Launch records matter beyond the definition:
// an offset launch listens on a port the definition alone cannot name, and a
// record keeps a since-edited definition stoppable. Invalid specs are skipped
// so stopping degrades gracefully on old or hand-edited records.
func stopTargetPorts(envDef map[string]any, launches []any, envID string) []int {
	seen := map[int]bool{}
	addSpec := func(spec any) {
		if ports, err := parsePortSpec(spec); err == nil {
			for _, p := range ports {
				seen[p] = true
			}
		}
	}
	for _, p := range processes(envDef) {
		addSpec(p["port"])
	}
	for _, recAny := range launches {
		rec, ok := recAny.(map[string]any)
		if !ok || pStr(rec, "env_id") != envID {
			continue
		}
		for _, procAny := range toAnySlice(rec["processes"]) {
			proc, ok := procAny.(map[string]any)
			if !ok {
				continue
			}
			if ap := toIntVal(proc["assigned_port"]); ap != 0 {
				seen[ap] = true
				continue
			}
			addSpec(proc["port"])
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
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
	envDef, err := c.findEnv(envID)
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
	for _, port := range stopTargetPorts(envDef, toAnySlice(data["launches"]), envID) {
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
	for _, envAny := range toAnySlice(envsDoc["environments"]) {
		envDef, ok := envAny.(map[string]any)
		if !ok {
			continue
		}
		st := EnvStatus{ID: pStr(envDef, "id"), Name: pStr(envDef, "name"), Processes: len(processes(envDef))}
		for _, port := range stopTargetPorts(envDef, launches, st.ID) {
			if _, ok := live[port]; ok {
				st.LivePorts = append(st.LivePorts, port)
			}
		}
		out = append(out, st)
	}
	return out, nil
}
