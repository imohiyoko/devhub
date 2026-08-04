package envs

// Tests for applying a switch plan. Nothing real is started or stopped: the
// compose adapter and the terminal spawn are both fakes, so these assert what
// devhub would do — which operations, in which order, scoped to what.

import (
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
)

// applyDoc mixes both component kinds: a shared compose database, and two
// scenarios whose host process depends on it.
func applyDoc() map[string]any {
	return map[string]any{
		"version": 2,
		"environments": []any{map[string]any{
			"id": "micro", "name": "Micro",
			"components": []any{
				map[string]any{"id": "db", "label": "DB", "kind": "compose_service", "lifecycle": "shared",
					"compose": map[string]any{"cwd": "~/platform", "project": "platform-local", "services": []any{"mysql"}}},
				map[string]any{"id": "acc", "command": "run-acc", "port": 4000, "delay_seconds": 0,
					"depends_on": []any{"db"}},
				map[string]any{"id": "bill", "command": "run-bill", "port": 5000, "delay_seconds": 0,
					"depends_on": []any{"db"}},
			},
			"scenarios": []any{
				map[string]any{"id": "accounting", "components": []any{"acc"}},
				map[string]any{"id": "billing", "components": []any{"bill"}},
			},
		}},
	}
}

func applyRequest(t *testing.T, c *Controller, body map[string]any) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/envs/switch/apply", nil)
	if err := c.HandlePost(rr, req, body); err != nil {
		t.Fatalf("apply: %v", err)
	}
	return decodeJSON(t, rr)
}

// resultActions flattens the response to "<action>:<id>" in execution order.
func resultActions(t *testing.T, resp map[string]any) []string {
	t.Helper()
	var out []string
	for _, r := range toAnySlice(resp["results"]) {
		entry := r.(map[string]any)
		out = append(out, pStr(entry, "action")+":"+pStr(entry, "id"))
	}
	return out
}

// TestApplySwitchStopsThenStartsKeepingShared is the flagship use case
// executed: switching scenarios stops the outgoing host process, starts the
// incoming one, and never touches the shared compose database.
func TestApplySwitchStopsThenStartsKeepingShared(t *testing.T) {
	compose := &fakeCompose{states: map[string]map[string]componentState{
		"platform-local": {"mysql": stateRunning},
	}}
	ports := &fakePorts{open: []portsctl.PortEntry{{Port: 4000, PID: 42}}} // acc is running
	c, spawned := newTestController(&fakeStore{envs: applyDoc()}, testDeps{ports: ports, compose: compose})

	resp := applyRequest(t, c, map[string]any{"env_id": "micro", "scenario_id": "billing"})
	if resp["ok"] != true {
		t.Errorf("ok = %v, results = %v", resp["ok"], resp["results"])
	}
	if got := resultActions(t, resp); !slices.Equal(got, []string{"stop:acc", "start:bill"}) {
		t.Errorf("operations = %v, want [stop:acc start:bill]", got)
	}
	// The shared database was neither stopped nor started.
	if len(compose.stops) != 0 || len(compose.ups) != 0 {
		t.Errorf("shared compose service touched: stops=%v ups=%v", compose.stops, compose.ups)
	}
	// Stopping a host component kills the listener on its declared port.
	if !slices.Equal(ports.kills, []killCall{{4000, 42}}) {
		t.Errorf("kills = %v, want acc's listener", ports.kills)
	}
	if cmds := spawned.all(); len(cmds) != 1 || !strings.Contains(spawnedCommandLine(cmds[0]), "run-bill") {
		t.Errorf("spawned = %v, want only bill", cmds)
	}
	// The started host process is recorded, so the UI lists it and a later
	// stop can find it.
	store := c.store.(*fakeStore)
	if len(store.launches) != 1 {
		t.Fatalf("launches = %d, want the started process recorded", len(store.launches))
	}
	procs := toAnySlice(store.launches[0].(map[string]any)["processes"])
	if len(procs) != 1 || pStr(procs[0].(map[string]any), "id") != "bill" {
		t.Errorf("record = %v, want only the started process", procs)
	}
}

// TestApplySwitchStartsComposeBeforeDependent pins cross-kind ordering: a host
// process that depends on a compose service starts after `up --wait` returns.
func TestApplySwitchStartsComposeBeforeDependent(t *testing.T) {
	compose := &fakeCompose{} // nothing running: db must come up too
	c, _ := newTestController(&fakeStore{envs: applyDoc()}, testDeps{compose: compose})

	resp := applyRequest(t, c, map[string]any{"env_id": "micro", "scenario_id": "accounting"})
	if got := resultActions(t, resp); !slices.Equal(got, []string{"start:db", "start:acc"}) {
		t.Errorf("operations = %v, want the compose service first", got)
	}
	if !slices.Equal(compose.ups, []string{"platform-local/mysql"}) {
		t.Errorf("ups = %v, want the declared project and services only", compose.ups)
	}
}

// TestApplySwitchStopsScenarioScopedOnly: an empty selection means "only the
// shared components stay", so both host processes stop — in the reverse of
// the planned order — and the shared database is left alone.
func TestApplySwitchStopsScenarioScopedOnly(t *testing.T) {
	compose := &fakeCompose{states: map[string]map[string]componentState{
		"platform-local": {"mysql": stateRunning},
	}}
	ports := &fakePorts{open: []portsctl.PortEntry{{Port: 4000, PID: 42}, {Port: 5000, PID: 43}}}
	c, _ := newTestController(&fakeStore{envs: applyDoc()}, testDeps{ports: ports, compose: compose})

	resp := applyRequest(t, c, map[string]any{"env_id": "micro", "components": []any{}})
	// Dependency order is db → acc → bill, so stopping walks it backwards.
	if got := resultActions(t, resp); !slices.Equal(got, []string{"stop:bill", "stop:acc"}) {
		t.Errorf("operations = %v, want reverse dependency order [stop:bill stop:acc]", got)
	}
	if len(compose.stops) != 0 {
		t.Errorf("shared compose service stopped: %v", compose.stops)
	}
}

// TestApplySwitchReportsPartialFailure: one failing operation neither aborts
// the rest nor is hidden.
func TestApplySwitchReportsPartialFailure(t *testing.T) {
	compose := &fakeCompose{upErr: errors.New("Cannot connect to the Docker daemon")}
	c, spawned := newTestController(&fakeStore{envs: applyDoc()}, testDeps{compose: compose})

	resp := applyRequest(t, c, map[string]any{"env_id": "micro", "scenario_id": "accounting"})
	if resp["ok"] != false {
		t.Errorf("ok = %v, want false when an operation failed", resp["ok"])
	}
	results := toAnySlice(resp["results"])
	if len(results) != 2 {
		t.Fatalf("results = %v, want both operations reported", results)
	}
	db := results[0].(map[string]any)
	if db["ok"] != false || !strings.Contains(pStr(db, "error"), "Cannot connect") {
		t.Errorf("db result = %v, want the failure with docker's message", db)
	}
	// The dependent still ran: apply reports a partial switch instead of
	// silently stopping halfway.
	acc := results[1].(map[string]any)
	if acc["ok"] != true {
		t.Errorf("acc result = %v, want the remaining start attempted", acc)
	}
	if len(spawned.all()) != 1 {
		t.Errorf("spawns = %d, want the host process still attempted", len(spawned.all()))
	}
}

// TestApplySwitchRefusesStalePlan: the fingerprint from the plan the user
// approved must still describe reality, or the stops they saw listed may not
// be the stops that would run.
func TestApplySwitchRefusesStalePlan(t *testing.T) {
	ports := &fakePorts{open: []portsctl.PortEntry{{Port: 4000, PID: 42}}}
	c, _ := newTestController(&fakeStore{envs: applyDoc()}, testDeps{ports: ports})

	rr := httptest.NewRecorder()
	planReq := httptest.NewRequest("POST", "/api/envs/switch/plan", nil)
	if err := c.HandlePost(rr, planReq, map[string]any{"env_id": "micro", "scenario_id": "billing"}); err != nil {
		t.Fatalf("plan: %v", err)
	}
	fingerprint := pStr(decodeJSON(t, rr), "fingerprint")
	if fingerprint == "" {
		t.Fatal("plan must carry a fingerprint")
	}

	// Applying the plan the user just saw is accepted.
	resp := applyRequest(t, c, map[string]any{
		"env_id": "micro", "scenario_id": "billing", "fingerprint": fingerprint,
	})
	if resp["ok"] != true {
		t.Errorf("a current plan must apply, got %v", resp)
	}

	// A plan computed against a different state is refused.
	err := c.HandlePost(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/envs/switch/apply", nil),
		map[string]any{"env_id": "micro", "scenario_id": "billing", "fingerprint": "0000000000000000"})
	if err == nil || !strings.Contains(err.Error(), "状態が変化しています") {
		t.Errorf("stale fingerprint err = %v, want a refusal", err)
	}
}

// TestApplySwitchHostStartFailureIsReported: a missing worktree fails only the
// host processes, and says so per component rather than failing the request.
func TestApplySwitchMissingWorktreeFailsHostStartsOnly(t *testing.T) {
	doc := applyDoc()
	env := doc["environments"].([]any)[0].(map[string]any)
	env["worktree"] = map[string]any{"enabled": true, "repo_path": "/repo/a", "branch": "gone"}
	compose := &fakeCompose{}
	c, spawned := newTestController(&fakeStore{envs: doc}, testDeps{compose: compose})

	resp := applyRequest(t, c, map[string]any{"env_id": "micro", "scenario_id": "accounting"})
	if resp["ok"] != false {
		t.Errorf("ok = %v, want false", resp["ok"])
	}
	results := toAnySlice(resp["results"])
	db := results[0].(map[string]any)
	if db["ok"] != true {
		t.Errorf("the compose service does not need a worktree, got %v", db)
	}
	acc := results[1].(map[string]any)
	if acc["ok"] != false || !strings.Contains(pStr(acc, "error"), "worktree") {
		t.Errorf("acc result = %v, want the worktree failure", acc)
	}
	if len(spawned.all()) != 0 {
		t.Errorf("nothing should be spawned without a resolved worktree, got %v", spawned.all())
	}
}

func TestApplySwitchRequestValidation(t *testing.T) {
	c, _ := newTestController(&fakeStore{envs: applyDoc()}, testDeps{})
	req := httptest.NewRequest("POST", "/api/envs/switch/apply", nil)
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"missing env_id", map[string]any{"scenario_id": "billing"}, "env_id is required"},
		{"no target", map[string]any{"env_id": "micro"}, "exactly one of scenario_id or components"},
		{"unknown scenario", map[string]any{"env_id": "micro", "scenario_id": "ghost"}, "Scenario 'ghost' not found"},
		{"unknown env", map[string]any{"env_id": "ghost", "scenario_id": "billing"}, "not found"},
	} {
		if err := c.HandlePost(httptest.NewRecorder(), req, tc.body); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}
