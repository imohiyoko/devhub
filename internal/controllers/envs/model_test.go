package envs

// Characterization tests for the lenient document decode: the typed model must
// read a stored v1 document with exactly the zero-value tolerance the map
// helpers (pStr/pMap/toStringSlice) had, so old or hand-edited documents keep
// launching the way they always did.

import (
	"reflect"
	"testing"
	"time"
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
	})
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
