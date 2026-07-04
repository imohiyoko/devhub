package envs

import "errors"

func findLaunch(launches []any, launchID string) map[string]any {
	for _, l := range launches {
		if m, ok := l.(map[string]any); ok && pStr(m, "launch_id") == launchID {
			return m
		}
	}
	return nil
}

// enrichLaunches returns launch records annotated with live worktree/port status.
func (c *Controller) enrichLaunches() (map[string]any, error) {
	data, err := c.store.LoadLaunches()
	if err != nil {
		return nil, err
	}
	portIndex := c.livePortIndex()
	enriched := []any{}
	for _, recAny := range toAnySlice(data["launches"]) {
		rec, ok := recAny.(map[string]any)
		if !ok {
			continue
		}
		wt := pStr(rec, "worktree_path")
		rec["worktree_exists"] = wt != "" && isDir(wt)
		procs := []any{}
		for _, procAny := range toAnySlice(rec["processes"]) {
			proc, ok := procAny.(map[string]any)
			if !ok {
				continue
			}
			var specPorts []int
			if ap, present := proc["assigned_port"]; present && ap != nil && toIntVal(ap) != 0 {
				specPorts = []int{toIntVal(ap)}
			} else if sp, err := parsePortSpec(proc["port"]); err == nil {
				specPorts = sp
			}
			live := []any{}
			for _, p := range specPorts {
				if pid, ok := portIndex[p]; ok {
					live = append(live, map[string]any{"port": p, "pid": pid})
				}
			}
			proc["live_ports"] = live
			proc["running"] = len(live) > 0
			pwt := pStr(proc, "worktree_path")
			proc["worktree_exists"] = pwt != "" && isDir(pwt)
			procs = append(procs, proc)
		}
		rec["processes"] = procs
		enriched = append(enriched, rec)
	}
	return map[string]any{"launches": enriched}, nil
}

// removeLaunch drops a launch record from the registry (never touches worktrees).
func (c *Controller) removeLaunch(launchID string, _ bool) error {
	data, err := c.store.LoadLaunches()
	if err != nil {
		return err
	}
	if findLaunch(toAnySlice(data["launches"]), launchID) == nil {
		return errors.New("launch record not found")
	}
	return c.store.RemoveLaunch(launchID)
}

// openLaunch opens a launch's worktree in the editor or a terminal.
func (c *Controller) openLaunch(launchID, target string) error {
	data, err := c.store.LoadLaunches()
	if err != nil {
		return err
	}
	rec := findLaunch(toAnySlice(data["launches"]), launchID)
	if rec == nil {
		return errors.New("launch record not found")
	}
	wt := pStr(rec, "worktree_path")
	if wt == "" || !isDir(wt) {
		return errors.New("worktree directory does not exist")
	}
	switch target {
	case "editor":
		c.workspace.OpenInEditor(wt)
	case "terminal":
		return c.openTerminalInDir(wt)
	default:
		return errors.New("invalid target")
	}
	return nil
}
