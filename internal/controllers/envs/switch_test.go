package envs

// Tests for the switch planner: what devhub believes is running now
// (componentStates, a port approximation) and the stop/keep/start difference
// it would apply to reach a scenario's target state (planSwitch).

import (
	"slices"
	"strings"
	"testing"
)

// stepIDs flattens a plan section to ids so tests can assert order directly.
func stepIDs(steps []PlanStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.ID)
	}
	return out
}

// switchEnv is the fixture the plan's flagship use case is built from: a
// shared database plus two scenario-scoped APIs, one per scenario.
func switchEnv() environment {
	return decodeEnvironment(map[string]any{
		"id": "micro", "name": "Micro",
		"components": []any{
			map[string]any{"id": "db", "label": "DB", "lifecycle": "shared", "command": "run-db", "port": float64(3000)},
			map[string]any{"id": "accounting-api", "command": "run-acc", "port": float64(4000), "depends_on": []any{"db"}},
			map[string]any{"id": "billing-api", "command": "run-bill", "port": float64(5000), "depends_on": []any{"db"}},
		},
		"scenarios": []any{
			map[string]any{"id": "accounting", "name": "会計", "components": []any{"accounting-api"}},
			map[string]any{"id": "billing", "name": "請求", "components": []any{"billing-api"}},
		},
	}, 2)
}

func TestComponentStates(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id": "dev",
		"components": []any{
			map[string]any{"id": "up", "command": "run", "port": float64(3000)},
			map[string]any{"id": "down", "command": "run", "port": float64(3100)},
			map[string]any{"id": "portless", "command": "run"},
			map[string]any{"id": "broken", "command": "run", "port": "abc"},
			map[string]any{"id": "offset", "command": "run", "port": float64(4000),
				"port_strategy": "offset", "port_env_var": "PORT"},
			map[string]any{"id": "svc", "kind": "compose_service",
				"compose": map[string]any{"cwd": "~/p", "project": "p", "services": []any{"s"}}},
		},
	}, 2)
	// The offset launch listens on the port its record pinned down (4001), not
	// on its declared base port.
	launches := []any{map[string]any{"env_id": "dev", "processes": []any{
		map[string]any{"id": "offset", "port": float64(4000), "assigned_port": float64(4001)},
	}}}
	live := map[int]int{3000: 11, 4001: 22}

	states := componentStates(env, launches, live)
	for id, want := range map[string]componentState{
		"up": stateRunning, "down": stateStopped, "portless": stateUnknown,
		"broken": stateUnknown, "offset": stateRunning, "svc": stateUnknown,
	} {
		if got := states[id].State; got != want {
			t.Errorf("%s state = %q, want %q", id, got, want)
		}
	}
	if states["portless"].Reason == "" || states["svc"].Reason == "" {
		t.Errorf("an unknown state must explain itself, got %+v / %+v", states["portless"], states["svc"])
	}
	if states["up"].Reason != "" {
		t.Errorf("an observed state needs no reason, got %q", states["up"].Reason)
	}
}

// TestPlanSwitchKeepsShared is the flagship case: switching scenarios stops
// the outgoing API, starts the incoming one, and leaves the shared database
// running — it is neither stopped nor restarted.
func TestPlanSwitchKeepsShared(t *testing.T) {
	states := map[string]componentStatus{
		"db":             {State: stateRunning},
		"accounting-api": {State: stateRunning},
		"billing-api":    {State: stateStopped},
	}
	plan, err := planSwitch(switchEnv(), switchRequest{ScenarioID: "billing"}, states)
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if !slices.Equal(stepIDs(plan.Stop), []string{"accounting-api"}) {
		t.Errorf("stop = %v, want [accounting-api]", stepIDs(plan.Stop))
	}
	if !slices.Equal(stepIDs(plan.Keep), []string{"db"}) {
		t.Errorf("keep = %v, want [db] (the shared component must not restart)", stepIDs(plan.Keep))
	}
	if !slices.Equal(stepIDs(plan.Start), []string{"billing-api"}) {
		t.Errorf("start = %v, want [billing-api]", stepIDs(plan.Start))
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", plan.Warnings)
	}
	if plan.EnvID != "micro" || plan.ScenarioID != "billing" {
		t.Errorf("plan header = %s/%s", plan.EnvID, plan.ScenarioID)
	}
	if !plan.Keep[0].Shared || plan.Keep[0].Label != "DB" {
		t.Errorf("kept step lost its metadata: %+v", plan.Keep[0])
	}
}

// TestPlanSwitchReapplyIsIdempotent pins the plan-level half of "re-applying
// the same scenario changes nothing".
func TestPlanSwitchReapplyIsIdempotent(t *testing.T) {
	states := map[string]componentStatus{
		"db":             {State: stateRunning},
		"accounting-api": {State: stateRunning},
		"billing-api":    {State: stateStopped},
	}
	plan, err := planSwitch(switchEnv(), switchRequest{ScenarioID: "accounting"}, states)
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if len(plan.Stop) != 0 || len(plan.Start) != 0 {
		t.Errorf("re-applying the running scenario must be a no-op, got stop=%v start=%v",
			stepIDs(plan.Stop), stepIDs(plan.Start))
	}
	if !slices.Equal(stepIDs(plan.Keep), []string{"db", "accounting-api"}) {
		t.Errorf("keep = %v", stepIDs(plan.Keep))
	}
}

// TestPlanSwitchOrdersByDependency: starts follow dependencies, stops reverse
// them so a dependent never outlives what it depends on.
func TestPlanSwitchOrdersByDependency(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id": "chain",
		"components": []any{
			map[string]any{"id": "web", "command": "run", "port": float64(3002), "depends_on": []any{"api"}},
			map[string]any{"id": "api", "command": "run", "port": float64(3001), "depends_on": []any{"db"}},
			map[string]any{"id": "db", "command": "run", "port": float64(3000)},
		},
		"scenarios": []any{map[string]any{"id": "all", "components": []any{"web", "api", "db"}}},
	}, 2)

	stopped := map[string]componentStatus{
		"web": {State: stateStopped}, "api": {State: stateStopped}, "db": {State: stateStopped},
	}
	plan, err := planSwitch(env, switchRequest{ScenarioID: "all"}, stopped)
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if !slices.Equal(stepIDs(plan.Start), []string{"db", "api", "web"}) {
		t.Errorf("start = %v, want dependency order [db api web]", stepIDs(plan.Start))
	}

	running := map[string]componentStatus{
		"web": {State: stateRunning}, "api": {State: stateRunning}, "db": {State: stateRunning},
	}
	// An empty selection targets the shared components only — here, none.
	plan, err = planSwitch(env, switchRequest{Components: []string{}}, running)
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if !slices.Equal(stepIDs(plan.Stop), []string{"web", "api", "db"}) {
		t.Errorf("stop = %v, want reverse dependency order [web api db]", stepIDs(plan.Stop))
	}
}

// TestPlanSwitchPullsInDependencies: a scenario names its entry points, and
// the components those need come along even when the scenario omits them.
func TestPlanSwitchPullsInDependencies(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id": "dev",
		"components": []any{
			map[string]any{"id": "worker", "command": "run", "port": float64(3001)},
			map[string]any{"id": "api", "command": "run", "port": float64(3000), "depends_on": []any{"worker"}},
		},
		"scenarios": []any{map[string]any{"id": "s", "components": []any{"api"}}}, // worker unlisted
	}, 2)
	plan, err := planSwitch(env, switchRequest{ScenarioID: "s"}, map[string]componentStatus{
		"api": {State: stateStopped}, "worker": {State: stateStopped},
	})
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if !slices.Equal(stepIDs(plan.Start), []string{"worker", "api"}) {
		t.Errorf("start = %v, want the dependency pulled in first", stepIDs(plan.Start))
	}
}

// TestPlanSwitchUnknownState pins plan §5: a component whose state devhub
// cannot observe is never stopped automatically, and is started with a warning
// when the target needs it.
func TestPlanSwitchUnknownState(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id": "dev",
		"components": []any{
			map[string]any{"id": "ghost", "command": "run"}, // no port: unobservable
			map[string]any{"id": "api", "command": "run", "port": float64(3000)},
		},
		"scenarios": []any{
			map[string]any{"id": "api-only", "components": []any{"api"}},
			map[string]any{"id": "ghost-only", "components": []any{"ghost"}},
		},
	}, 2)
	states := map[string]componentStatus{
		"ghost": {stateUnknown, "監視できるポートが宣言されていません"},
		"api":   {State: stateStopped},
	}

	plan, err := planSwitch(env, switchRequest{ScenarioID: "api-only"}, states)
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if len(plan.Stop) != 0 {
		t.Errorf("an unobservable component must not be stopped, got %v", stepIDs(plan.Stop))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "自動停止の対象にしません") {
		t.Errorf("warnings = %v, want one explaining the skipped stop", plan.Warnings)
	}

	plan, err = planSwitch(env, switchRequest{ScenarioID: "ghost-only"}, states)
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if !slices.Equal(stepIDs(plan.Start), []string{"ghost"}) {
		t.Errorf("start = %v, want the unobservable component started", stepIDs(plan.Start))
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "重複") {
		t.Errorf("warnings = %v, want one about a possible duplicate start", plan.Warnings)
	}
}

// TestPlanSwitchV1Environment: a v1 document switches through its generated
// default scenario, so the switch paths work before any migration.
func TestPlanSwitchV1Environment(t *testing.T) {
	env := decodeEnvironment(map[string]any{
		"id": "dev",
		"processes": []any{
			map[string]any{"id": "db", "command": "run-db", "port": float64(3000)},
			map[string]any{"id": "api", "command": "run-api", "port": float64(4000), "depends_on": []any{"db"}},
		},
	}, 1)
	plan, err := planSwitch(env, switchRequest{ScenarioID: defaultScenarioID}, map[string]componentStatus{
		"db": {State: stateRunning}, "api": {State: stateStopped},
	})
	if err != nil {
		t.Fatalf("planSwitch: %v", err)
	}
	if !slices.Equal(stepIDs(plan.Keep), []string{"db"}) || !slices.Equal(stepIDs(plan.Start), []string{"api"}) {
		t.Errorf("keep/start = %v / %v", stepIDs(plan.Keep), stepIDs(plan.Start))
	}
}

func TestPlanSwitchErrors(t *testing.T) {
	env := switchEnv()
	cases := []struct {
		name string
		env  environment
		req  switchRequest
		want string
	}{
		{"unknown scenario", env, switchRequest{ScenarioID: "ghost"}, "Scenario 'ghost' not found"},
		{"unknown component", env, switchRequest{Components: []string{"ghost"}}, "Component 'ghost' not found"},
		{"dependency cycle", decodeEnvironment(map[string]any{
			"id": "bad",
			"components": []any{
				map[string]any{"id": "a", "command": "run", "depends_on": []any{"b"}},
				map[string]any{"id": "b", "command": "run", "depends_on": []any{"a"}},
			},
		}, 2), switchRequest{Components: []string{}}, "Circular dependency"},
		{"dangling dependency", decodeEnvironment(map[string]any{
			"id":         "bad",
			"components": []any{map[string]any{"id": "a", "command": "run", "depends_on": []any{"ghost"}}},
		}, 2), switchRequest{Components: []string{}}, "Dependency 'ghost' for component 'a' not found"},
	}
	for _, c := range cases {
		_, err := planSwitch(c.env, c.req, map[string]componentStatus{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
	}
}
