package envs

// The typed v1 model. The stored document stays map[string]any at the
// boundaries — validateEnvs checks the raw shape at save time, SaveEnvs
// persists exactly what the UI sent, and launch records pass through
// enrichLaunches untouched so unknown fields survive — but everything behind
// findEnv works on these types.
//
// Decoding is deliberately lenient: it mirrors the zero-value semantics of the
// pStr/pMap/toStringSlice helpers (a malformed field reads as empty, never an
// error), so launching keeps tolerating old or hand-edited documents exactly
// as the map-based code did.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Component kinds and lifecycles (config schema v2).
const (
	kindHostProcess     = "host_process"
	kindComposeService  = "compose_service"
	lifecycleShared     = "shared"
	lifecycleScenario   = "scenario"
	defaultScenarioID   = "default"
	defaultScenarioName = "デフォルト"
)

// environment is one entry of the envs document's environments array.
//
// Components/Scenarios and Processes are two views of the same definition,
// each derived from the other at decode time: a v1 document's processes
// become host_process components in one default scenario, and a v2 document's
// host_process components become Processes so the existing launch/stop/status
// paths keep working unchanged. compose_service components have no process
// view (no declared port) — they are handled by the switch paths only.
type environment struct {
	ID         string
	Name       string
	Worktree   worktree
	Processes  []process
	Components []component
	Scenarios  []scenario
}

// component is the switchable unit of start/stop/status: a host process (the
// v1 process shape plus kind/lifecycle) or a Docker Compose service.
type component struct {
	ID        string
	Label     string
	Kind      string // kindHostProcess | kindComposeService
	Shared    bool   // lifecycle "shared": kept across scenario switches
	DependsOn []string
	Proc      process     // host_process payload (zero for compose_service)
	Compose   composeSpec // compose_service payload (zero for host_process)
}

// composeSpec locates a compose_service: where to run docker compose, which
// files, under which project name (the ownership marker devhub operates on),
// and which services.
type composeSpec struct {
	Cwd      string
	Files    []string
	Project  string
	Services []string
}

// scenario is a named set of scenario-scoped component ids; shared components
// are implicitly part of every scenario's target state and are not listed.
type scenario struct {
	ID         string
	Name       string
	Components []string
}

// worktree is the environment-level worktree binding.
type worktree struct {
	Enabled  bool
	RepoPath string
	Branch   string
}

// binding is a per-process worktree binding.
type binding struct {
	RepoPath string
	Branch   string
}

// envVar is one {key, value} pair of a process's declared env. The document
// keeps them as an ordered array (not an object) so the user's input order
// survives the save round-trip; the order is preserved here, and building the
// final env map keeps the map's last-duplicate-wins semantics.
type envVar struct{ Key, Value string }

// process is one process definition inside an environment.
type process struct {
	ID           string
	Label        string
	Command      string
	Cwd          string
	Port         any // raw v1 port spec (number/string/range); parsePortSpec expands it, launch records store it verbatim
	PortStrategy string
	PortEnvVar   string
	DependsOn    []string
	Delay        time.Duration // pause after this process before the next spawn
	Env          []envVar
	Binding      binding
}

func (p process) isOffset() bool {
	return p.PortStrategy == "offset" && p.PortEnvVar != ""
}

// docVersion reads the document's schema version: absent or unrecognized
// reads as 1 (every pre-versioning document is v1). This is deliberately
// lenient like the rest of the decode layer; validateEnvs is the strict gate
// that keeps unsupported versions out of the store. Only an exact 2 selects
// the v2 decode — a future version's components would carry semantics this
// build does not know, so it falls back to v1 (an empty environment) rather
// than launching them under v2 rules.
func docVersion(doc map[string]any) int {
	if toIntVal(doc["version"]) == 2 {
		return 2
	}
	return 1
}

// decodeEnvironments decodes the document's environments array, skipping
// non-object entries (as every map-based iteration did).
func decodeEnvironments(doc map[string]any) []environment {
	version := docVersion(doc)
	var out []environment
	for _, e := range toAnySlice(doc["environments"]) {
		if m, ok := e.(map[string]any); ok {
			out = append(out, decodeEnvironment(m, version))
		}
	}
	return out
}

func decodeEnvironment(m map[string]any, version int) environment {
	env := environment{ID: pStr(m, "id"), Name: pStr(m, "name")}
	wt := pMap(m, "worktree")
	enabled, _ := wt["enabled"].(bool)
	env.Worktree = worktree{Enabled: enabled, RepoPath: pStr(wt, "repo_path"), Branch: pStr(wt, "branch")}
	if version >= 2 {
		for _, cm := range objSlice(m["components"]) {
			env.Components = append(env.Components, decodeComponent(cm))
		}
		for _, sm := range objSlice(m["scenarios"]) {
			env.Scenarios = append(env.Scenarios, scenario{
				ID: pStr(sm, "id"), Name: pStr(sm, "name"), Components: toStringSlice(sm["components"]),
			})
		}
		for _, c := range env.Components {
			if c.Kind == kindHostProcess {
				env.Processes = append(env.Processes, c.Proc)
			}
		}
		return env
	}
	// v1: processes are the definition; wrap them as scenario-scoped
	// host_process components in a single default scenario, so the switch
	// paths see every document through the same model.
	env.Processes = decodeProcesses(processes(m))
	ids := make([]string, 0, len(env.Processes))
	for _, p := range env.Processes {
		env.Components = append(env.Components, component{
			ID: p.ID, Label: p.Label, Kind: kindHostProcess, DependsOn: p.DependsOn, Proc: p,
		})
		ids = append(ids, p.ID)
	}
	if len(ids) > 0 {
		env.Scenarios = []scenario{{ID: defaultScenarioID, Name: defaultScenarioName, Components: ids}}
	}
	return env
}

// decodeComponent reads one v2 component. A host_process component is the v1
// process shape plus kind/lifecycle, so its payload decodes through
// decodeProcess on the same map; an absent kind reads as host_process.
func decodeComponent(m map[string]any) component {
	c := component{
		ID:        pStr(m, "id"),
		Label:     pStr(m, "label"),
		Kind:      pStr(m, "kind"),
		Shared:    pStr(m, "lifecycle") == lifecycleShared,
		DependsOn: toStringSlice(m["depends_on"]),
	}
	if c.Kind == "" {
		c.Kind = kindHostProcess
	}
	switch c.Kind {
	case kindComposeService:
		cm := pMap(m, "compose")
		c.Compose = composeSpec{
			Cwd:      pStr(cm, "cwd"),
			Files:    toStringSlice(cm["files"]),
			Project:  pStr(cm, "project"),
			Services: toStringSlice(cm["services"]),
		}
	case kindHostProcess:
		c.Proc = decodeProcess(m)
	}
	return c
}

func decodeProcesses(ms []map[string]any) []process {
	var out []process
	for _, m := range ms {
		out = append(out, decodeProcess(m))
	}
	return out
}

func decodeProcess(m map[string]any) process {
	p := process{
		ID:           pStr(m, "id"),
		Label:        pStr(m, "label"),
		Command:      pStr(m, "command"),
		Cwd:          pStr(m, "cwd"),
		Port:         m["port"],
		PortStrategy: pStr(m, "port_strategy"),
		PortEnvVar:   pStr(m, "port_env_var"),
		DependsOn:    toStringSlice(m["depends_on"]),
		Delay:        processDelay(m),
	}
	bm := pMap(m, "binding")
	p.Binding = binding{RepoPath: pStr(bm, "repo_path"), Branch: pStr(bm, "branch")}
	for _, item := range toAnySlice(m["env"]) {
		em, ok := item.(map[string]any)
		if !ok {
			continue
		}
		k := pStr(em, "key")
		if k == "" {
			continue
		}
		val := ""
		if raw, ok := em["value"]; ok && raw != nil {
			val = fmt.Sprintf("%v", raw)
		}
		p.Env = append(p.Env, envVar{Key: k, Value: val})
	}
	return p
}

// processDelay coerces delay_seconds (number or numeric string) to a duration:
// missing or unparsable falls back to 1s, negative clamps to 0.
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
