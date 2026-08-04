package envs

// Characterization tests for the lenient document decode: the typed model must
// read a stored v1 document with exactly the zero-value tolerance the map
// helpers (pStr/pMap/toStringSlice) had, so old or hand-edited documents keep
// launching the way they always did.

import (
	"reflect"
	"testing"
	"time"

	"github.com/imohiyoko/devhub/internal/container"
)

func TestDecodeEnvironmentLenient(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id":   "dev",
		"name": "Dev",
		"worktree": map[string]any{
			"enabled": true, "repo_path": "/repo/a", "branch": "feat",
		},
		"processes": []any{
			map[string]any{
				"id": "api", "label": "API", "command": "run", "cwd": "~/app",
				"port": "3000-3001", "port_strategy": "offset", "port_env_var": "PORT",
				"depends_on": []any{"db", 42, "cache"}, // non-string dep entries are skipped
				"env": []any{
					map[string]any{"key": "A", "value": float64(3000)}, // numeric value formats as 3000
					map[string]any{"key": "B"},                         // missing value reads as ""
					map[string]any{"value": "orphan"},                  // empty key is dropped
					"not-a-pair",                                       // non-object entries are skipped
					map[string]any{"key": "A", "value": "second"},      // duplicates keep order; map build last-wins
				},
				"binding": map[string]any{"repo_path": "/repo/b", "branch": "x"},
			},
			"not-a-process",  // non-object process entries are skipped
			map[string]any{}, // an empty process decodes to zero values, default delay
		},
	}, 1)
	if env.ID != "dev" || env.Name != "Dev" {
		t.Errorf("env id/name = %q/%q", env.ID, env.Name)
	}
	if env.Worktree != (worktree{Enabled: true, RepoPath: "/repo/a", Branch: "feat"}) {
		t.Errorf("worktree = %+v", env.Worktree)
	}
	if len(env.Processes) != 2 {
		t.Fatalf("processes = %d, want 2 (non-object entry skipped)", len(env.Processes))
	}
	p := env.Processes[0]
	if p.ID != "api" || p.Label != "API" || p.Command != "run" || p.Cwd != "~/app" {
		t.Errorf("process fields = %+v", p)
	}
	if p.Port != "3000-3001" {
		t.Errorf("Port must keep the raw spec verbatim, got %#v", p.Port)
	}
	if !p.isOffset() {
		t.Error("offset strategy with env var should report isOffset")
	}
	if !reflect.DeepEqual(p.DependsOn, []string{"db", "cache"}) {
		t.Errorf("DependsOn = %v", p.DependsOn)
	}
	wantEnv := []envVar{{"A", "3000"}, {"B", ""}, {"A", "second"}}
	if !reflect.DeepEqual(p.Env, wantEnv) {
		t.Errorf("Env = %v, want %v", p.Env, wantEnv)
	}
	if got := processEnv(p.Env, nil); got["A"] != "second" {
		t.Errorf("duplicate env key must be last-wins, got A=%q", got["A"])
	}
	if p.Binding != (binding{RepoPath: "/repo/b", Branch: "x"}) {
		t.Errorf("Binding = %+v", p.Binding)
	}
	empty := env.Processes[1]
	if empty.ID != "" || empty.Port != nil || empty.Delay != time.Second {
		t.Errorf("empty process = %+v, want zero values with 1s delay", empty)
	}
}

func TestDecodeEnvironmentsSkipsMalformed(t *testing.T) {
	envs := decodeEnvironments(map[string]any{"environments": []any{
		map[string]any{"id": "a"},
		"junk",
		map[string]any{"id": "b", "worktree": "not-an-object"}, // malformed worktree reads as unbound
	}})
	if len(envs) != 2 || envs[0].ID != "a" || envs[1].ID != "b" {
		t.Fatalf("envs = %+v", envs)
	}
	if envs[1].Worktree != (worktree{}) {
		t.Errorf("malformed worktree = %+v, want zero", envs[1].Worktree)
	}
}

// TestDecodeV1ConvertsToComponents pins the v1→component conversion: every
// process becomes a scenario-scoped host_process component, gathered into one
// default scenario, so the switch paths see v1 documents through the same model.
func TestDecodeV1ConvertsToComponents(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id": "dev",
		"processes": []any{
			map[string]any{"id": "db", "label": "DB", "command": "run-db", "port": float64(3000)},
			map[string]any{"id": "api", "depends_on": []any{"db"}},
		},
	}, 1)
	if len(env.Components) != 2 {
		t.Fatalf("components = %+v, want 2", env.Components)
	}
	db := env.Components[0]
	if db.ID != "db" || db.Label != "DB" || db.Kind != kindHostProcess || db.Shared {
		t.Errorf("db component = %+v", db)
	}
	if db.Proc.Command != "run-db" || db.Proc.Port != float64(3000) {
		t.Errorf("db payload = %+v", db.Proc)
	}
	if !reflect.DeepEqual(env.Components[1].DependsOn, []string{"db"}) {
		t.Errorf("api deps = %v", env.Components[1].DependsOn)
	}
	if len(env.Scenarios) != 1 {
		t.Fatalf("scenarios = %+v, want the default scenario", env.Scenarios)
	}
	def := env.Scenarios[0]
	if def.ID != defaultScenarioID || !reflect.DeepEqual(def.Components, []string{"db", "api"}) {
		t.Errorf("default scenario = %+v", def)
	}

	empty := decodeEnvironment(map[string]any{"id": "idle"}, 1)
	if len(empty.Components) != 0 || len(empty.Scenarios) != 0 {
		t.Errorf("empty env must have no components/scenarios, got %+v / %+v", empty.Components, empty.Scenarios)
	}
}

// TestDecodeV2Environment pins the v2 decode: compose payloads, the shared
// lifecycle, scenarios, and the derived Processes view (host_process only)
// that keeps the existing launch paths working.
func TestDecodeV2Environment(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id": "micro", "name": "Micro",
		"components": []any{
			map[string]any{"id": "mysql", "label": "MySQL", "kind": "compose_service", "lifecycle": "shared",
				"compose": map[string]any{"cwd": "~/platform", "files": []any{"compose.yml"}, "project": "platform-local", "services": []any{"mysql"}}},
			map[string]any{"id": "api", "command": "run-api", "port": float64(3000), "depends_on": []any{"mysql"}}, // kind absent = host_process
		},
		"scenarios": []any{
			map[string]any{"id": "accounting", "name": "会計", "components": []any{"api"}},
		},
	}, 2)
	if len(env.Components) != 2 {
		t.Fatalf("components = %+v", env.Components)
	}
	mysql := env.Components[0]
	if mysql.Kind != kindComposeService || !mysql.Shared {
		t.Errorf("mysql = %+v, want shared compose_service", mysql)
	}
	wantCompose := container.ComposeSpec{Cwd: "~/platform", Files: []string{"compose.yml"}, Project: "platform-local", Services: []string{"mysql"}}
	if !reflect.DeepEqual(mysql.Compose, wantCompose) {
		t.Errorf("compose = %+v, want %+v", mysql.Compose, wantCompose)
	}
	api := env.Components[1]
	if api.Kind != kindHostProcess || api.Shared || api.Proc.Command != "run-api" {
		t.Errorf("api = %+v, want host_process with payload", api)
	}
	// Only the host_process component appears in the Processes view.
	if len(env.Processes) != 1 || env.Processes[0].ID != "api" {
		t.Errorf("processes view = %+v, want [api]", env.Processes)
	}
	if len(env.Scenarios) != 1 || env.Scenarios[0].ID != "accounting" || !reflect.DeepEqual(env.Scenarios[0].Components, []string{"api"}) {
		t.Errorf("scenarios = %+v", env.Scenarios)
	}
}

func TestDecodeRuntime(t *testing.T) {
	// No runtime block: the environment keeps using whatever Docker context
	// the user's shell resolves to, which is what devhub did before runtimes
	// existed.
	if got := decodeRuntime(map[string]any{}); got != (container.Spec{Provider: container.ProviderDocker}) {
		t.Errorf("absent runtime = %+v, want the docker provider", got)
	}

	full := decodeRuntime(map[string]any{"runtime": map[string]any{
		"provider": "colima", "profile": "development", "engine": "containerd"}})
	if full != (container.Spec{Provider: container.ProviderColima, Profile: "development", Engine: container.EngineContainerd}) {
		t.Errorf("runtime = %+v", full)
	}

	// A v1 document has no runtime block, and one pasted into a hand-edited v1
	// document must not be reported as an execution base the v1 path honours.
	v1 := decodeEnvironment(map[string]any{
		"id": "legacy", "runtime": map[string]any{"provider": "colima"},
	}, 1)
	if v1.Runtime != (container.Spec{Provider: container.ProviderDocker}) {
		t.Errorf("v1 runtime = %+v, want the default", v1.Runtime)
	}
}

func TestDocVersion(t *testing.T) {
	cases := []struct {
		doc  map[string]any
		want int
	}{
		{map[string]any{}, 1},
		{map[string]any{"version": float64(1)}, 1},
		{map[string]any{"version": float64(2)}, 2},
		{map[string]any{"version": "2"}, 1},        // lenient decode: non-numeric reads as v1; validateEnvs is the strict gate
		{map[string]any{"version": float64(3)}, 1}, // a future schema must not decode as v2
	}
	for _, c := range cases {
		if got := docVersion(c.doc); got != c.want {
			t.Errorf("docVersion(%v) = %d, want %d", c.doc, got, c.want)
		}
	}
}

func TestProcessDelayCoercion(t *testing.T) {
	cases := []struct {
		raw  any
		want time.Duration
	}{
		{nil, time.Second}, // explicit null falls back
		{float64(0), 0},    // 0 means no pause
		{float64(2.5), 2500 * time.Millisecond},
		{int(2), 2 * time.Second},
		{"0.5", 500 * time.Millisecond}, // numeric strings parse
		{" 3 ", 3 * time.Second},        // with surrounding whitespace
		{"junk", time.Second},           // unparsable falls back
		{float64(-4), 0},                // negative clamps to 0
		{true, time.Second},             // unsupported type falls back
	}
	for _, c := range cases {
		if got := processDelay(map[string]any{"delay_seconds": c.raw}); got != c.want {
			t.Errorf("processDelay(%v) = %v, want %v", c.raw, got, c.want)
		}
	}
	if got := processDelay(map[string]any{}); got != time.Second {
		t.Errorf("missing delay_seconds = %v, want 1s", got)
	}
}
