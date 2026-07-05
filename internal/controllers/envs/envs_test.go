package envs

import (
	"reflect"
	"strings"
	"testing"
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
	procs := []map[string]any{
		{"id": "a"},
		{"id": "b", "depends_on": []any{"a"}},
		{"id": "c", "depends_on": []any{"a", "b"}},
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
	if _, err := topoSort([]map[string]any{
		{"id": "a", "depends_on": []any{"b"}},
		{"id": "b", "depends_on": []any{"a"}},
	}); err == nil {
		t.Error("topoSort should error on a cycle")
	}
	if _, err := topoSort([]map[string]any{
		{"id": "a", "depends_on": []any{"ghost"}},
	}); err == nil {
		t.Error("topoSort should error on an unknown dep")
	}
}

func TestProcessEnv(t *testing.T) {
	def := map[string]any{"env": []any{
		map[string]any{"key": "DEVHUB_HOME", "value": "~/.devhub-verify"}, // leading ~ expands like cwd does
		map[string]any{"key": "PLAIN", "value": "literal"},                // non-~ value passes through untouched
	}}
	got := processEnv(def, map[string]string{"PORT": "3001"})
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
