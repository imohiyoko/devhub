package envs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/pathutil"
)

const portPlaceholder = "{{port}}"

// applyPortPlaceholder substitutes {{port}} in command when a port was assigned.
func applyPortPlaceholder(command string, assignedPort *int) string {
	if assignedPort == nil || command == "" {
		return command
	}
	return strings.ReplaceAll(command, portPlaceholder, strconv.Itoa(*assignedPort))
}

// parsePortSpec expands a 'port' field into concrete ports. Accepts nil/""/int/
// numeric string/"a-b" range. Doubles as the save-time validator. Mirrors
// _parse_port_spec.
func parsePortSpec(spec any) ([]int, error) {
	switch v := spec.(type) {
	case nil:
		return []int{}, nil
	case bool:
		return nil, errors.New("invalid port")
	case float64:
		i := int(v)
		if float64(i) != v {
			return nil, errors.New("invalid port")
		}
		return validatePorts([]int{i})
	case int:
		return validatePorts([]int{v})
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return []int{}, nil
		}
		if strings.Contains(s, "-") {
			a, b, ok := strings.Cut(s, "-")
			if !ok {
				return nil, errors.New("invalid port")
			}
			lo, err1 := strconv.Atoi(strings.TrimSpace(a))
			hi, err2 := strconv.Atoi(strings.TrimSpace(b))
			if err1 != nil || err2 != nil {
				return nil, errors.New("invalid port")
			}
			if lo > hi {
				lo, hi = hi, lo
			}
			ports := make([]int, 0, hi-lo+1)
			for p := lo; p <= hi; p++ {
				ports = append(ports, p)
			}
			return validatePorts(ports)
		}
		i, err := strconv.Atoi(s)
		if err != nil {
			return nil, errors.New("invalid port")
		}
		return validatePorts([]int{i})
	default:
		return nil, errors.New("invalid port")
	}
}

func validatePorts(ports []int) ([]int, error) {
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return nil, errors.New("port out of range")
		}
	}
	if len(ports) > 1000 {
		return nil, errors.New("port range too large")
	}
	return ports, nil
}

func isOffset(proc map[string]any) bool {
	return pStr(proc, "port_strategy") == "offset" && pStr(proc, "port_env_var") != ""
}

func assignPort(base int, taken map[int]bool) int {
	for port := base; port <= 65535 && port-base < 200; port++ {
		if !taken[port] {
			return port
		}
	}
	return base // window exhausted; proceed (may collide)
}

// assignPorts maps {process_id: assigned_port} for offset processes, reserving
// each within the batch. Mirrors _assign_ports.
func (c *Controller) assignPorts(envDef map[string]any, live map[int]int) map[string]int {
	taken := map[int]bool{}
	for p := range live {
		taken[p] = true
	}
	assigned := map[string]int{}
	for _, p := range processes(envDef) {
		if !isOffset(p) {
			continue
		}
		ports, err := parsePortSpec(p["port"])
		if err != nil || len(ports) == 0 {
			continue
		}
		port := assignPort(ports[0], taken)
		assigned[pStr(p, "id")] = port
		taken[port] = true
	}
	return assigned
}

// livePortIndex maps declared port -> listening pid (first listener wins).
func (c *Controller) livePortIndex() map[int]int {
	index := map[int]int{}
	list, err := c.ports.ListOpen()
	if err != nil {
		return index
	}
	for _, p := range list {
		if _, ok := index[p.Port]; !ok {
			index[p.Port] = p.PID
		}
	}
	return index
}

// killPortsFor frees the declared ports of the given processes (best-effort).
func (c *Controller) killPortsFor(procs []map[string]any) {
	live := c.livePortIndex()
	killed := false
	for _, proc := range procs {
		ports, err := parsePortSpec(proc["port"])
		if err != nil {
			continue
		}
		for _, p := range ports {
			if pid, ok := live[p]; ok {
				if err := c.ports.KillPortProcess(p, pid); err == nil {
					killed = true
				}
			}
		}
	}
	if killed {
		time.Sleep(500 * time.Millisecond)
	}
}

// resolveWorktree resolves (repo, branch) to an existing worktree path, or "".
func (c *Controller) resolveWorktree(repoPath, branch string) string {
	repo := pathutil.ExpandUser(repoPath)
	if repo == "" || branch == "" {
		return ""
	}
	worktrees, err := c.git.ListWorktrees(repo)
	if err != nil {
		return ""
	}
	for _, wt := range worktrees {
		b, _ := wt["branch"].(string)
		exists, _ := wt["exists"].(bool)
		if b == branch && exists {
			p, _ := wt["path"].(string)
			return p
		}
	}
	return ""
}

// setupWorktree resolves the env-level worktree binding to an existing path.
// Returns "" when none is configured; errors when one is configured but missing.
func (c *Controller) setupWorktree(worktreeDef map[string]any) (string, error) {
	if worktreeDef == nil {
		return "", nil
	}
	if enabled, _ := worktreeDef["enabled"].(bool); !enabled {
		return "", nil
	}
	repoPath := pStr(worktreeDef, "repo_path")
	branch := pStr(worktreeDef, "branch")
	if repoPath == "" || branch == "" {
		return "", nil
	}
	wt := c.resolveWorktree(repoPath, branch)
	if wt == "" {
		return "", fmt.Errorf("branch '%s' の worktree が見つかりません（%s）。git tool で作成してください。", branch, repoPath)
	}
	return wt, nil
}

// resolveCwds builds {process_id: cwd}; a bound process must have an existing
// worktree (error otherwise), an unbound one inherits envCwdOverride.
func (c *Controller) resolveCwds(envDef map[string]any, envCwdOverride string) (map[string]string, error) {
	cwds := map[string]string{}
	for _, p := range processes(envDef) {
		binding := pMap(p, "binding")
		repo := pStr(binding, "repo_path")
		branch := pStr(binding, "branch")
		if repo != "" && branch != "" {
			wt := c.resolveWorktree(repo, branch)
			if wt == "" {
				return nil, fmt.Errorf("process '%s': branch '%s' の worktree が見つかりません（%s）。git tool で作成してください。", pStr(p, "id"), branch, repo)
			}
			cwds[pStr(p, "id")] = wt
		} else {
			cwds[pStr(p, "id")] = envCwdOverride
		}
	}
	return cwds, nil
}

// topoOrder runs Kahn's algorithm over the processes' depends_on edges and
// returns them in dependency order. The returned order is valid only when both
// unknownDep and cyclic are zero/false. On an unknown dependency it reports the
// missing dep and the process that referenced it; on a cycle it sets cyclic.
// Callers (validateDeps, topoSort) format their own error messages so they can
// scope them to an environment id without duplicating the algorithm.
func topoOrder(procs []map[string]any) (order []string, unknownDep, badProc string, cyclic bool) {
	inDegree := map[string]int{}
	adj := map[string][]string{}
	for _, p := range procs {
		id := pStr(p, "id")
		inDegree[id] = 0
		adj[id] = nil
	}
	for _, p := range procs {
		id := pStr(p, "id")
		for _, dep := range toStringSlice(p["depends_on"]) {
			if _, ok := adj[dep]; !ok {
				return nil, dep, id, false
			}
			adj[dep] = append(adj[dep], id)
			inDegree[id]++
		}
	}
	var queue []string
	for _, p := range procs { // iterate procs (stable order) to mirror dict insertion order
		id := pStr(p, "id")
		if inDegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, nxt := range adj[id] {
			inDegree[nxt]--
			if inDegree[nxt] == 0 {
				queue = append(queue, nxt)
			}
		}
	}
	if len(order) != len(procs) {
		return nil, "", "", true
	}
	return order, "", "", false
}

// topoSort returns process ids in dependency order, or an error on a cycle / unknown dep.
func topoSort(procs []map[string]any) ([]string, error) {
	order, unknownDep, badProc, cyclic := topoOrder(procs)
	if unknownDep != "" {
		return nil, fmt.Errorf("Dependency '%s' for process '%s' not found in environment", unknownDep, badProc)
	}
	if cyclic {
		return nil, errors.New("Circular dependency detected in depends_on")
	}
	return order, nil
}

// runProcesses topologically sorts and launches the env's processes on a
// goroutine, with per-process delays. Mirrors _run_processes.
func (c *Controller) runProcesses(envDef map[string]any, cwdByPid map[string]string, cwdOverride string, envByPid map[string]map[string]string, portByPid map[string]int) error {
	procs := processes(envDef)
	sorted, err := topoSort(procs)
	if err != nil {
		return err
	}
	pidToDef := map[string]map[string]any{}
	for _, p := range procs {
		pidToDef[pStr(p, "id")] = p
	}
	go func() {
		for i, pid := range sorted {
			pDef := pidToDef[pid]
			cwd, ok := cwdByPid[pid]
			if !ok {
				cwd = cwdOverride
			}
			var assignedPort *int
			if v, ok := portByPid[pid]; ok {
				vv := v
				assignedPort = &vv
			}
			c.launchProcess(pDef, cwd, envByPid[pid], assignedPort)
			if i < len(sorted)-1 {
				time.Sleep(processDelay(pDef))
			}
		}
	}()
	return nil
}

func processDelay(pDef map[string]any) time.Duration {
	raw, ok := pDef["delay_seconds"]
	if !ok || raw == nil {
		return time.Second
	}
	var sec float64 = 1.0
	switch v := raw.(type) {
	case float64:
		sec = v
	case int:
		sec = float64(v)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			sec = 1.0
		} else {
			sec = f
		}
	}
	if sec < 0 {
		sec = 0
	}
	return time.Duration(sec * float64(time.Second))
}

// launchProcess resolves cwd/env/port for a single process and opens a terminal.
func (c *Controller) launchProcess(processDef map[string]any, cwdOverride string, extraEnv map[string]string, assignedPort *int) {
	cwd := cwdOverride
	if cwd == "" {
		if raw := pStr(processDef, "cwd"); raw != "" {
			cwd = pathutil.ExpandUser(raw)
		}
	}
	command := applyPortPlaceholder(pStr(processDef, "command"), assignedPort)
	c.openInTerminal(cwd, command, processEnv(processDef, extraEnv))
}

// processEnv builds a process's resolved environment: its declared env (with a
// leading ~ expanded in values, so e.g. DEVHUB_HOME=~/.devhub-verify points at
// the home dir like cwd does) overlaid by extraEnv (e.g. the offset port var).
//
// env is an ordered list of {key, value} pairs (a JSON array) so the user's
// input order survives the save round-trip — a JSON object would be re-sorted
// alphabetically by encoding/json on save.
func processEnv(processDef map[string]any, extraEnv map[string]string) map[string]string {
	env := map[string]string{}
	for _, item := range toAnySlice(processDef["env"]) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		k := pStr(m, "key")
		if k == "" {
			continue
		}
		val := ""
		if raw, ok := m["value"]; ok && raw != nil {
			val = fmt.Sprintf("%v", raw)
		}
		env[k] = pathutil.ExpandUser(val)
	}
	maps.Copy(env, extraEnv)
	return env
}

// recordLaunch appends a launch record to the registry under the registry lock.
func (c *Controller) recordLaunch(envDef map[string]any, worktreePath string, cwds map[string]string, assigned map[string]int) error {
	wt := pMap(envDef, "worktree")
	enabled, _ := wt["enabled"].(bool)
	repoPath := ""
	branch := ""
	if enabled {
		repoPath = pathutil.ExpandUser(pStr(wt, "repo_path"))
		branch = pStr(wt, "branch")
	}
	procRecords := []any{}
	for _, p := range processes(envDef) {
		id := pStr(p, "id")
		binding := pMap(p, "binding")
		label := pStr(p, "label")
		if label == "" {
			label = id
		}
		var assignedPort any
		if ap, ok := assigned[id]; ok {
			assignedPort = ap
		}
		procRecords = append(procRecords, map[string]any{
			"id":            id,
			"label":         label,
			"command":       pStr(p, "command"),
			"port":          p["port"],
			"worktree_path": orNil(cwds[id]),
			"repo_path":     pStr(binding, "repo_path"),
			"branch":        pStr(binding, "branch"),
			"assigned_port": assignedPort,
		})
	}
	name := pStr(envDef, "name")
	if name == "" {
		name = pStr(envDef, "id")
	}
	record := map[string]any{
		"launch_id":     time.Now().Format("20060102-150405-") + tokenHex(3),
		"env_id":        pStr(envDef, "id"),
		"env_name":      name,
		"worktree_path": orNil(worktreePath),
		"repo_path":     repoPath,
		"branch":        branch,
		"launched_at":   time.Now().Format(time.RFC3339),
		"processes":     procRecords,
	}
	return c.store.AppendLaunch(record)
}

// launchEnvironment resolves worktrees/ports and launches an environment.
func (c *Controller) launchEnvironment(envID string) error {
	envDef, err := c.findEnv(envID)
	if err != nil {
		return err
	}
	envCwd, err := c.setupWorktree(pMap(envDef, "worktree"))
	if err != nil {
		return err
	}
	cwds, err := c.resolveCwds(envDef, envCwd)
	if err != nil {
		return err
	}
	var batonProcs []map[string]any
	for _, p := range processes(envDef) {
		if !isOffset(p) {
			batonProcs = append(batonProcs, p)
		}
	}
	c.killPortsFor(batonProcs)
	assigned := c.assignPorts(envDef, c.livePortIndex())
	envByPid := map[string]map[string]string{}
	for _, p := range processes(envDef) {
		id := pStr(p, "id")
		if port, ok := assigned[id]; ok {
			envByPid[id] = map[string]string{pStr(p, "port_env_var"): strconv.Itoa(port)}
		}
	}
	if err := c.recordLaunch(envDef, envCwd, cwds, assigned); err != nil {
		return err
	}
	return c.runProcesses(envDef, cwds, envCwd, envByPid, assigned)
}

func tokenHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
