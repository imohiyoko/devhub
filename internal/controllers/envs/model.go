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

// environment is one entry of the envs document's environments array.
type environment struct {
	ID        string
	Name      string
	Worktree  worktree
	Processes []process
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

// decodeEnvironments decodes the document's environments array, skipping
// non-object entries (as every map-based iteration did).
func decodeEnvironments(doc map[string]any) []environment {
	var out []environment
	for _, e := range toAnySlice(doc["environments"]) {
		if m, ok := e.(map[string]any); ok {
			out = append(out, decodeEnvironment(m))
		}
	}
	return out
}

func decodeEnvironment(m map[string]any) environment {
	env := environment{ID: pStr(m, "id"), Name: pStr(m, "name")}
	wt := pMap(m, "worktree")
	enabled, _ := wt["enabled"].(bool)
	env.Worktree = worktree{Enabled: enabled, RepoPath: pStr(wt, "repo_path"), Branch: pStr(wt, "branch")}
	env.Processes = decodeProcesses(processes(m))
	return env
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
