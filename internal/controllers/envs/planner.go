package envs

// The pure planning half of launching: everything here computes over the typed
// model and observed state (live port index, resolved cwds) without touching
// the store, the network, or processes. The side effects live in executor.go;
// launch.go orchestrates the two. Keeping this half pure is what lets the
// scenario switch planner (stop/keep/start diffs) build on the same seam.

import (
	"errors"
	"fmt"
	"maps"
	"sort"
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
func assignPorts(procs []process, live map[int]int) map[string]int {
	taken := map[int]bool{}
	for p := range live {
		taken[p] = true
	}
	assigned := map[string]int{}
	for _, p := range procs {
		if !p.isOffset() {
			continue
		}
		ports, err := parsePortSpec(p.Port)
		if err != nil || len(ports) == 0 {
			continue
		}
		port := assignPort(ports[0], taken)
		assigned[p.ID] = port
		taken[port] = true
	}
	return assigned
}

// BatonKill records one process killed to free a declared (baton) port.
type BatonKill struct{ Port, PID int }

// batonKillTargets finds the live listeners on the given processes' declared
// ports — what a baton launch must kill to take the ports over. Offset
// processes are skipped: they get a fresh port assigned instead of taking
// their declared one over.
func batonKillTargets(procs []process, live map[int]int) []BatonKill {
	var targets []BatonKill
	for _, proc := range procs {
		if proc.isOffset() {
			continue
		}
		ports, err := parsePortSpec(proc.Port)
		if err != nil {
			continue
		}
		for _, p := range ports {
			if pid, ok := live[p]; ok {
				targets = append(targets, BatonKill{Port: p, PID: pid})
			}
		}
	}
	return targets
}

// depNode is the (id, depends_on) view the dependency sort runs over —
// processes and components share the algorithm through it.
type depNode struct {
	id   string
	deps []string
}

func procNodes(procs []process) []depNode {
	nodes := make([]depNode, 0, len(procs))
	for _, p := range procs {
		nodes = append(nodes, depNode{id: p.ID, deps: p.DependsOn})
	}
	return nodes
}

func componentNodes(comps []component) []depNode {
	nodes := make([]depNode, 0, len(comps))
	for _, c := range comps {
		nodes = append(nodes, depNode{id: c.ID, deps: c.DependsOn})
	}
	return nodes
}

// topoOrder runs Kahn's algorithm over the nodes' dependency edges and
// returns them in dependency order. The returned order is valid only when both
// unknownDep and cyclic are zero/false. On an unknown dependency it reports the
// missing dep and the node that referenced it; on a cycle it sets cyclic.
// Callers (validateDeps, validateComponentDeps, topoSort) format their own
// error messages so they can scope them to an environment id and a node noun
// without duplicating the algorithm.
func topoOrder(nodes []depNode) (order []string, unknownDep, badNode string, cyclic bool) {
	inDegree := map[string]int{}
	adj := map[string][]string{}
	for _, n := range nodes {
		inDegree[n.id] = 0
		adj[n.id] = nil
	}
	for _, n := range nodes {
		for _, dep := range n.deps {
			if _, ok := adj[dep]; !ok {
				return nil, dep, n.id, false
			}
			adj[dep] = append(adj[dep], n.id)
			inDegree[n.id]++
		}
	}
	var queue []string
	for _, n := range nodes { // iterate nodes (stable order) to mirror dict insertion order
		if inDegree[n.id] == 0 {
			queue = append(queue, n.id)
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
	if len(order) != len(nodes) {
		return nil, "", "", true
	}
	return order, "", "", false
}

// topoSort returns process ids in dependency order, or an error on a cycle / unknown dep.
func topoSort(procs []process) ([]string, error) {
	order, unknownDep, badProc, cyclic := topoOrder(procNodes(procs))
	if unknownDep != "" {
		return nil, fmt.Errorf("Dependency '%s' for process '%s' not found in environment", unknownDep, badProc)
	}
	if cyclic {
		return nil, errors.New("Circular dependency detected in depends_on")
	}
	return order, nil
}

// spawnStep is one fully-resolved terminal launch: the executor only has to
// open a terminal with these values and pause delay before the next step.
type spawnStep struct {
	cwd     string
	command string
	env     map[string]string
	delay   time.Duration
}

// spawnStepFor resolves one process into a spawnStep: the cwd falls back to
// the process's own cwd (with ~ expanded) when no worktree cwd applies, and an
// assigned offset port is substituted into the command and injected through
// the process's port env var on top of its declared env.
func spawnStepFor(p process, cwd string, assigned map[string]int) spawnStep {
	if cwd == "" && p.Cwd != "" {
		cwd = pathutil.ExpandUser(p.Cwd)
	}
	var extraEnv map[string]string
	var assignedPort *int
	if port, ok := assigned[p.ID]; ok {
		extraEnv = map[string]string{p.PortEnvVar: strconv.Itoa(port)}
		v := port
		assignedPort = &v
	}
	return spawnStep{
		cwd:     cwd,
		command: applyPortPlaceholder(p.Command, assignedPort),
		env:     processEnv(p.Env, extraEnv),
		delay:   p.Delay,
	}
}

// planSpawns computes the environment's launch sequence: processes in
// dependency order, each resolved to a spawnStep. cwds comes from worktree
// resolution (resolveCwds, which covers every process), assigned from offset
// port assignment.
func planSpawns(procs []process, cwds map[string]string, assigned map[string]int) ([]spawnStep, error) {
	sorted, err := topoSort(procs)
	if err != nil {
		return nil, err
	}
	byID := map[string]process{}
	for _, p := range procs {
		byID[p.ID] = p
	}
	steps := make([]spawnStep, 0, len(sorted))
	for _, pid := range sorted {
		steps = append(steps, spawnStepFor(byID[pid], cwds[pid], assigned))
	}
	return steps, nil
}

// processEnv builds a process's resolved environment: its declared env pairs
// (with a leading ~ expanded in values, so e.g. DEVHUB_HOME=~/.devhub-verify
// points at the home dir like cwd does) overlaid by extraEnv (e.g. the offset
// port var).
func processEnv(vars []envVar, extraEnv map[string]string) map[string]string {
	env := map[string]string{}
	for _, v := range vars {
		env[v.Key] = pathutil.ExpandUser(v.Value)
	}
	maps.Copy(env, extraEnv)
	return env
}

// portsByProcess maps process id -> the ports that identify that process:
// its declared port spec plus the ports its launch records pin down
// (assigned_port when the launch got an offset port, the recorded spec
// otherwise — the same precedence the launch list uses for its live badge).
// Launch records matter beyond the definition: an offset launch listens on a
// port the definition alone cannot name, and a record keeps a since-edited
// definition stoppable, so ids that exist only in records are kept too.
// Invalid specs are skipped, so stopping and status degrade gracefully on old
// or hand-edited records.
func portsByProcess(env environment, launches []any) map[string][]int {
	seen := map[string]map[int]bool{}
	addSpec := func(id string, spec any) {
		ports, err := parsePortSpec(spec)
		if err != nil {
			return
		}
		for _, p := range ports {
			if seen[id] == nil {
				seen[id] = map[int]bool{}
			}
			seen[id][p] = true
		}
	}
	for _, p := range env.Processes {
		addSpec(p.ID, p.Port)
	}
	for _, recAny := range launches {
		rec, ok := recAny.(map[string]any)
		if !ok || pStr(rec, "env_id") != env.ID {
			continue
		}
		for _, procAny := range toAnySlice(rec["processes"]) {
			proc, ok := procAny.(map[string]any)
			if !ok {
				continue
			}
			id := pStr(proc, "id")
			if ap := toIntVal(proc["assigned_port"]); ap != 0 {
				addSpec(id, ap)
				continue
			}
			addSpec(id, proc["port"])
		}
	}
	out := make(map[string][]int, len(seen))
	for id, ports := range seen {
		list := make([]int, 0, len(ports))
		for p := range ports {
			list = append(list, p)
		}
		sort.Ints(list)
		out[id] = list
	}
	return out
}

// stopTargetPorts computes the deduplicated, sorted candidate ports for
// stopping env: the union of every process's identifying ports.
func stopTargetPorts(env environment, launches []any) []int {
	seen := map[int]bool{}
	for _, ports := range portsByProcess(env, launches) {
		for _, p := range ports {
			seen[p] = true
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}
