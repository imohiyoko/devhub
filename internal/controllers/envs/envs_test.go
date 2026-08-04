package envs

import (
	"reflect"
	"strings"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/storage"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":               "''",
		"abc":            "abc",
		"/path/to-dir_1": "/path/to-dir_1",
		"a b":            "'a b'",
		"a$b":            "'a$b'",
		"it's":           `'it'"'"'s'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplescriptEscape(t *testing.T) {
	cases := map[string]string{
		`a"b`:    `a\"b`,
		`a\b`:    `a\\b`,
		"a\nb":   `a\nb`,
		"a\r\nb": `a\nb`,
	}
	for in, want := range cases {
		if got := applescriptEscape(in); got != want {
			t.Errorf("applescriptEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePortSpec(t *testing.T) {
	ok := []struct {
		in   any
		want []int
	}{
		{nil, []int{}},
		{"", []int{}},
		{float64(3000), []int{3000}},
		{"3000", []int{3000}},
		{"3000-3002", []int{3000, 3001, 3002}},
		{"3002-3000", []int{3000, 3001, 3002}},
	}
	for _, c := range ok {
		got, err := parsePortSpec(c.in)
		if err != nil {
			t.Errorf("parsePortSpec(%v) error: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePortSpec(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	bad := []any{true, float64(70000), "abc", float64(0), "1-2000"}
	for _, in := range bad {
		if _, err := parsePortSpec(in); err == nil {
			t.Errorf("parsePortSpec(%v) expected error", in)
		}
	}
}

func TestBuildCmdWithEnv(t *testing.T) {
	unix := buildCmdWithEnv("make run", map[string]string{"B": "2", "A": "1"}, false, false)
	if unix != "export A=1 && export B=2 && make run" {
		t.Errorf("unix = %q", unix)
	}
	unixQuoted := buildCmdWithEnv("cmd", map[string]string{"X": "a b"}, false, false)
	if unixQuoted != "export X='a b' && cmd" {
		t.Errorf("unixQuoted = %q", unixQuoted)
	}
	// PowerShell joins with a newline, not ';': the wt launch path splits its
	// command line on ';' (microsoft/terminal#11314), so it must never emit one.
	ps := buildCmdWithEnv("cmd", map[string]string{"PORT": "3000"}, true, true)
	if ps != "$env:PORT='3000'\ncmd" {
		t.Errorf("powershell = %q", ps)
	}
	cmdexe := buildCmdWithEnv("cmd", map[string]string{"PORT": "3000"}, true, false)
	if cmdexe != `set "PORT=3000" & cmd` {
		t.Errorf("cmd.exe = %q", cmdexe)
	}
	if none := buildCmdWithEnv("cmd", nil, false, false); none != "cmd" {
		t.Errorf("no-env = %q", none)
	}
}

func TestApplyPortPlaceholder(t *testing.T) {
	p := 4000
	if got := applyPortPlaceholder("app --port {{port}}", &p); got != "app --port 4000" {
		t.Errorf("got %q", got)
	}
	if got := applyPortPlaceholder("app --port {{port}}", nil); got != "app --port {{port}}" {
		t.Errorf("nil port should be untouched, got %q", got)
	}
}

func TestValidateDeps(t *testing.T) {
	good := []map[string]any{
		{"id": "a"},
		{"id": "b", "depends_on": []any{"a"}},
		{"id": "c", "depends_on": []any{"a", "b"}},
	}
	if err := validateDeps(good, "env1"); err != nil {
		t.Errorf("good deps errored: %v", err)
	}
	cycle := []map[string]any{
		{"id": "a", "depends_on": []any{"b"}},
		{"id": "b", "depends_on": []any{"a"}},
	}
	if err := validateDeps(cycle, "env1"); err == nil {
		t.Error("cycle should error")
	}
	unknown := []map[string]any{
		{"id": "a", "depends_on": []any{"ghost"}},
	}
	if err := validateDeps(unknown, "env1"); err == nil {
		t.Error("unknown dep should error")
	}
}

func TestTopoSort(t *testing.T) {
	// validateDeps and topoSort share topoOrder; this locks the ordering output
	// (deps before dependents, stable on insertion order) that validateDeps's
	// error-only assertions don't cover.
	procs := []process{
		{ID: "a"},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"a", "b"}},
	}
	got, err := topoSort(procs)
	if err != nil {
		t.Fatalf("topoSort errored: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("topoSort len = %d, want %d (%v)", len(got), len(want), got)
	}
	pos := map[string]int{}
	for i, id := range got {
		pos[id] = i
	}
	if !(pos["a"] < pos["b"] && pos["b"] < pos["c"]) {
		t.Errorf("topoSort order violates deps: %v", got)
	}
	if _, err := topoSort([]process{
		{ID: "a", DependsOn: []string{"b"}},
		{ID: "b", DependsOn: []string{"a"}},
	}); err == nil {
		t.Error("topoSort should error on a cycle")
	}
	if _, err := topoSort([]process{
		{ID: "a", DependsOn: []string{"ghost"}},
	}); err == nil {
		t.Error("topoSort should error on an unknown dep")
	}
}

func TestProcessEnv(t *testing.T) {
	def := decodeProcess(map[string]any{"env": []any{
		map[string]any{"key": "DEVHUB_HOME", "value": "~/.devhub-verify"}, // leading ~ expands like cwd does
		map[string]any{"key": "PLAIN", "value": "literal"},                // non-~ value passes through untouched
	}})
	got := processEnv(def.Env, map[string]string{"PORT": "3001"})
	if dh := got["DEVHUB_HOME"]; strings.HasPrefix(dh, "~") || !strings.HasSuffix(dh, ".devhub-verify") || dh == ".devhub-verify" {
		t.Errorf("DEVHUB_HOME not expanded from ~: %q", dh)
	}
	if got["PLAIN"] != "literal" {
		t.Errorf("PLAIN = %q, want literal", got["PLAIN"])
	}
	if got["PORT"] != "3001" {
		t.Errorf("PORT (extraEnv) = %q, want 3001", got["PORT"])
	}
}

// TestValidateEnvsAcceptsShippedExample guards backward compatibility: the
// document a first run is actually seeded with (envs.example.json through the
// real store's seeding path) must always pass save-time validation unchanged.
func TestValidateEnvsAcceptsShippedExample(t *testing.T) {
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	doc, err := st.LoadEnvs()
	if err != nil {
		t.Fatalf("LoadEnvs: %v", err)
	}
	if len(toAnySlice(doc["environments"])) == 0 {
		t.Fatalf("expected environments seeded from envs.example.json, got %v", doc)
	}
	if err := validateEnvs(doc); err != nil {
		t.Errorf("shipped example rejected by validateEnvs: %v", err)
	}
}

func TestValidateEnvs(t *testing.T) {
	// Valid: one env, one offset process with a base port and env var.
	good := map[string]any{"environments": []any{
		map[string]any{"id": "web", "processes": []any{
			map[string]any{"id": "api", "port": "3000", "port_strategy": "offset", "port_env_var": "PORT"},
		}},
	}}
	if err := validateEnvs(good); err != nil {
		t.Errorf("valid envs errored: %v", err)
	}
	// Offset without env var must fail.
	bad := map[string]any{"environments": []any{
		map[string]any{"id": "web", "processes": []any{
			map[string]any{"id": "api", "port": "3000", "port_strategy": "offset"},
		}},
	}}
	if err := validateEnvs(bad); err == nil {
		t.Error("offset without port_env_var should error")
	}
	// Bad env id.
	badID := map[string]any{"environments": []any{map[string]any{"id": "bad id!"}}}
	if err := validateEnvs(badID); err == nil {
		t.Error("invalid env id should error")
	}
	// repos[] scope: a binding inside the declared repos passes.
	inScope := map[string]any{"environments": []any{
		map[string]any{"id": "web", "repos": []any{"/repo/a", "/repo/b"}, "processes": []any{
			map[string]any{"id": "api", "binding": map[string]any{"repo_path": "/repo/a", "branch": "feature"}},
		}},
	}}
	if err := validateEnvs(inScope); err != nil {
		t.Errorf("in-scope binding errored: %v", err)
	}
	// repos[] scope: a binding outside the declared repos must fail.
	outScope := map[string]any{"environments": []any{
		map[string]any{"id": "web", "repos": []any{"/repo/a"}, "processes": []any{
			map[string]any{"id": "api", "binding": map[string]any{"repo_path": "/repo/x", "branch": "feature"}},
		}},
	}}
	if err := validateEnvs(outScope); err == nil {
		t.Error("out-of-scope binding repo should error")
	}
	// Empty/absent repos imposes no constraint (backward compatible).
	noScope := map[string]any{"environments": []any{
		map[string]any{"id": "web", "processes": []any{
			map[string]any{"id": "api", "binding": map[string]any{"repo_path": "/repo/x", "branch": "feature"}},
		}},
	}}
	if err := validateEnvs(noScope); err != nil {
		t.Errorf("no-scope binding errored: %v", err)
	}
	// repos[] scope also covers the env-level worktree repo_path.
	wtOut := map[string]any{"environments": []any{
		map[string]any{"id": "web", "repos": []any{"/repo/a"},
			"worktree": map[string]any{"repo_path": "/repo/x", "branch": "feature"}},
	}}
	if err := validateEnvs(wtOut); err == nil {
		t.Error("out-of-scope worktree repo_path should error")
	}
	// Malformed repos (not an array of strings) must fail.
	badRepos := map[string]any{"environments": []any{
		map[string]any{"id": "web", "repos": "nope"},
	}}
	if err := validateEnvs(badRepos); err == nil {
		t.Error("non-array repos should error")
	}
}

func TestValidateDocVersion(t *testing.T) {
	for _, doc := range []map[string]any{
		{}, // absent = v1
		{"version": float64(1), "environments": []any{}}, // explicit v1
		{"version": float64(2), "environments": []any{}}, // v2
	} {
		if err := validateEnvs(doc); err != nil {
			t.Errorf("validateEnvs(%v) errored: %v", doc, err)
		}
	}
	// A fractional version must be rejected, not truncated into a supported one.
	for _, v := range []any{float64(3), float64(1.5), float64(2.5), "2", true} {
		if err := validateEnvs(map[string]any{"version": v}); err == nil {
			t.Errorf("version %v should be rejected", v)
		}
	}
}

// v2Env wraps one v2 environment into a full document for validateEnvs.
func v2Doc(env map[string]any) map[string]any {
	return map[string]any{"version": float64(2), "environments": []any{env}}
}

// TestValidateEnvsV2AcceptsPlanExample feeds the design doc's own §8 example
// (trimmed) through save-time validation: the shape the plan promises must be
// accepted verbatim.
func TestValidateEnvsV2AcceptsPlanExample(t *testing.T) {
	doc := v2Doc(map[string]any{
		"id": "microservices-local", "name": "マイクロサービス検証環境",
		"runtime": map[string]any{"provider": "colima", "profile": "development", "engine": "docker"},
		"components": []any{
			map[string]any{"id": "mysql", "label": "MySQL", "kind": "compose_service", "lifecycle": "shared",
				"compose": map[string]any{"cwd": "~/projects/platform", "files": []any{"compose.yml"},
					"project": "platform-local", "services": []any{"mysql"}},
				"depends_on": []any{}},
			map[string]any{"id": "accounting-api", "kind": "compose_service",
				"compose": map[string]any{"cwd": "~/projects/accounting", "files": []any{"compose.yml"},
					"project": "accounting-local", "services": []any{"api"}},
				"depends_on": []any{"mysql"}},
			map[string]any{"id": "billing-api", "kind": "compose_service",
				"compose": map[string]any{"cwd": "~/projects/billing", "files": []any{"compose.yml"},
					"project": "billing-local", "services": []any{"api"}},
				"depends_on": []any{"mysql"}},
		},
		"scenarios": []any{
			map[string]any{"id": "accounting", "name": "会計", "components": []any{"accounting-api"}},
			map[string]any{"id": "billing", "name": "請求", "components": []any{"billing-api"}},
		},
	})
	if err := validateEnvs(doc); err != nil {
		t.Errorf("plan §8 example rejected: %v", err)
	}
}

func TestValidateEnvsV2(t *testing.T) {
	compose := func(project string) map[string]any {
		return map[string]any{"cwd": "~/p", "project": project, "services": []any{"svc"}}
	}
	valid := v2Doc(map[string]any{
		"id": "micro",
		"components": []any{
			map[string]any{"id": "db", "kind": "compose_service", "lifecycle": "shared", "compose": compose("p-local")},
			map[string]any{"id": "api", "command": "run", "port": float64(3000), "depends_on": []any{"db"}},
		},
		"scenarios": []any{map[string]any{"id": "main", "components": []any{"api"}}},
	})
	if err := validateEnvs(valid); err != nil {
		t.Fatalf("valid v2 errored: %v", err)
	}

	cases := []struct {
		name string
		env  map[string]any
		want string
	}{
		{"v2 env with processes", map[string]any{"id": "e", "processes": []any{}},
			"must not have processes"},
		{"non-object runtime", map[string]any{"id": "e", "runtime": "colima"},
			"runtime must be an object"},
		{"non-array components", map[string]any{"id": "e", "components": "nope"},
			"components must be an array"},
		{"non-object component", map[string]any{"id": "e", "components": []any{"nope"}},
			"components must be objects"},
		{"missing component id", map[string]any{"id": "e", "components": []any{map[string]any{"kind": "host_process"}}},
			"Component ID is required"},
		{"duplicate component id", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a"}, map[string]any{"id": "a"}}},
			"Duplicate component ID"},
		{"bad kind", map[string]any{"id": "e", "components": []any{map[string]any{"id": "a", "kind": "vm"}}},
			"kind must be"},
		{"bad lifecycle", map[string]any{"id": "e", "components": []any{map[string]any{"id": "a", "lifecycle": "forever"}}},
			"lifecycle must be"},
		{"compose without payload", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "kind": "compose_service"}}},
			"needs a compose object"},
		{"compose without cwd", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "kind": "compose_service", "compose": map[string]any{"project": "p", "services": []any{"s"}}}}},
			"needs a cwd"},
		{"compose without project", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "kind": "compose_service", "compose": map[string]any{"cwd": "~/p", "services": []any{"s"}}}}},
			"needs a project"},
		{"compose without services", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "kind": "compose_service", "compose": map[string]any{"cwd": "~/p", "project": "p"}}}},
			"at least one service"},
		{"host component with bad port", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "port": "abc"}}},
			"port must be"},
		{"host offset without env var", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "port": float64(3000), "port_strategy": "offset"}}},
			"port_env_var"},
		{"shared depending on scenario component", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "shared1", "lifecycle": "shared", "command": "run", "depends_on": []any{"scoped1"}},
			map[string]any{"id": "scoped1", "command": "run"}}},
			"Shared component 'shared1' cannot depend on scenario component 'scoped1'"},
		{"unknown dependency", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "depends_on": []any{"ghost"}}}},
			"Dependency 'ghost' for component 'a' not found"},
		// A malformed depends_on must be rejected, not silently decoded into
		// fewer edges than the author wrote.
		{"scalar depends_on", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "db"}, map[string]any{"id": "a", "depends_on": "db"}}},
			"depends_on must be an array of component ids"},
		{"depends_on with a non-string entry", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "db"}, map[string]any{"id": "a", "depends_on": []any{"db", float64(42)}}}},
			"depends_on must be an array of component ids"},
		{"dependency cycle", map[string]any{"id": "e", "components": []any{
			map[string]any{"id": "a", "depends_on": []any{"b"}},
			map[string]any{"id": "b", "depends_on": []any{"a"}}}},
			"Circular dependency"},
		{"non-array scenarios", map[string]any{"id": "e", "scenarios": "nope"},
			"scenarios must be an array"},
		{"missing scenario id", map[string]any{"id": "e", "scenarios": []any{map[string]any{"name": "x"}}},
			"Scenario ID is required"},
		{"duplicate scenario id", map[string]any{"id": "e", "scenarios": []any{
			map[string]any{"id": "s"}, map[string]any{"id": "s"}}},
			"Duplicate scenario ID"},
		{"scenario referencing unknown component", map[string]any{"id": "e", "scenarios": []any{
			map[string]any{"id": "s", "components": []any{"ghost"}}}},
			"references unknown component 'ghost'"},
		{"scenario with non-string component ref", map[string]any{"id": "e", "scenarios": []any{
			map[string]any{"id": "s", "components": []any{float64(1)}}}},
			"array of component ids"},
	}
	for _, c := range cases {
		err := validateEnvs(v2Doc(c.env))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
	}

	// Listing a shared component in a scenario is redundant but allowed.
	sharedListed := v2Doc(map[string]any{
		"id":         "e",
		"components": []any{map[string]any{"id": "db", "kind": "compose_service", "lifecycle": "shared", "compose": compose("p")}},
		"scenarios":  []any{map[string]any{"id": "s", "components": []any{"db"}}},
	})
	if err := validateEnvs(sharedListed); err != nil {
		t.Errorf("shared component listed in scenario should be allowed: %v", err)
	}
}
