package envs

// Tests for the Docker Compose adapter. Nothing here runs Docker: the command
// runner is a fake, so the assertions are about the argv devhub builds, how it
// reads `docker compose ps` output, and how it degrades when Docker is absent
// or the daemon is unreachable.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records the invocations and replays canned results.
type fakeRunner struct {
	calls  []runnerCall
	stdout string
	stderr string
	err    error
}

type runnerCall struct {
	cwd  string
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, cwd, name string, args ...string) (string, string, error) {
	f.calls = append(f.calls, runnerCall{cwd: cwd, name: name, args: args})
	return f.stdout, f.stderr, f.err
}

func testCompose(runner commandRunner) *dockerCompose {
	return &dockerCompose{
		runner:   runner,
		lookPath: func(string) (string, error) { return "/usr/local/bin/docker", nil },
	}
}

func TestComposeServiceStatesBuildsScopedArgv(t *testing.T) {
	runner := &fakeRunner{stdout: `[{"Service":"mysql","State":"running"}]`}
	spec := composeSpec{Cwd: "~/platform", Files: []string{"compose.yml", "compose.override.yml"},
		Project: "platform-local", Services: []string{"mysql"}}

	states, err := testCompose(runner).ServiceStates(context.Background(), spec)
	if err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	if states["mysql"] != stateRunning {
		t.Errorf("mysql = %q, want running", states["mysql"])
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "docker" {
		t.Errorf("binary = %q, want docker", call.name)
	}
	want := []string{"compose", "--project-name", "platform-local",
		"--file", "compose.yml", "--file", "compose.override.yml",
		"ps", "--format", "json", "--all"}
	if !slices.Equal(call.args, want) {
		t.Errorf("argv = %v,\nwant %v", call.args, want)
	}
	// The project name scopes every call: devhub must not be able to report on
	// containers the environment does not declare (plan §13).
	if !slices.Contains(call.args, "--project-name") {
		t.Error("the invocation must be scoped to the declared compose project")
	}
	if strings.HasPrefix(call.cwd, "~") || call.cwd == "" {
		t.Errorf("cwd = %q, want the expanded path", call.cwd)
	}
}

func TestComposeServiceStatesFailures(t *testing.T) {
	// Docker not installed: the probe never spawns anything.
	runner := &fakeRunner{}
	adapter := &dockerCompose{runner: runner, lookPath: func(string) (string, error) {
		return "", errors.New("exec: \"docker\": executable file not found in $PATH")
	}}
	_, err := adapter.ServiceStates(context.Background(), composeSpec{Project: "p"})
	if err == nil || !strings.Contains(err.Error(), "docker コマンドが見つかりません") {
		t.Errorf("missing docker: err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("a host without docker must not spawn anything, got %v", runner.calls)
	}

	// Daemon unreachable: Docker's own message becomes the reason, because
	// "not installed" and "daemon down" need different fixes.
	daemonDown := "failed to connect to the docker API at unix:///var/run/docker.sock; check if the daemon is running\nsecond line"
	runner = &fakeRunner{stderr: daemonDown, err: errors.New("exit status 1")}
	_, err = testCompose(runner).ServiceStates(context.Background(), composeSpec{Project: "p"})
	if err == nil || !strings.Contains(err.Error(), "failed to connect to the docker API") {
		t.Errorf("daemon down: err = %v, want docker's own message", err)
	}
	if err != nil && strings.Contains(err.Error(), "second line") {
		t.Errorf("only the first stderr line should surface, got %q", err)
	}

	// A failure with nothing on stderr still reports something actionable.
	runner = &fakeRunner{err: errors.New("context deadline exceeded")}
	_, err = testCompose(runner).ServiceStates(context.Background(), composeSpec{Project: "p"})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("silent failure: err = %v", err)
	}
}

func TestParseComposePS(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want map[string]componentState
	}{
		{"empty output", "", map[string]componentState{}},
		{"json array", `[{"Service":"a","State":"running"},{"Service":"b","State":"exited"}]`,
			map[string]componentState{"a": stateRunning, "b": stateStopped}},
		{"newline delimited", "{\"Service\":\"a\",\"State\":\"running\"}\n{\"Service\":\"b\",\"State\":\"created\"}\n",
			map[string]componentState{"a": stateRunning, "b": stateStopped}},
		{"empty array", "[]", map[string]componentState{}},
		// Replicas: one container down makes the whole service not running,
		// whichever order the entries arrive in.
		{"replica down after up", `[{"Service":"a","State":"running"},{"Service":"a","State":"exited"}]`,
			map[string]componentState{"a": stateStopped}},
		{"replica down before up", `[{"Service":"a","State":"exited"},{"Service":"a","State":"running"}]`,
			map[string]componentState{"a": stateStopped}},
		{"entry without a service name is skipped", `[{"State":"running"}]`, map[string]componentState{}},
	}
	for _, c := range cases {
		got, err := parseComposePS(c.out)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: states = %v, want %v", c.name, got, c.want)
			continue
		}
		for svc, state := range c.want {
			if got[svc] != state {
				t.Errorf("%s: %s = %q, want %q", c.name, svc, got[svc], state)
			}
		}
	}

	if _, err := parseComposePS("not json"); err == nil {
		t.Error("unparsable output must error rather than read as no services")
	}
	if _, err := parseComposePS(`[{"Service":`); err == nil {
		t.Error("a truncated array must error")
	}
}

func TestComposeComponentState(t *testing.T) {
	spec := composeSpec{Services: []string{"api", "worker"}}
	cases := []struct {
		name     string
		services map[string]componentState
		want     componentState
	}{
		{"all running", map[string]componentState{"api": stateRunning, "worker": stateRunning}, stateRunning},
		{"one missing", map[string]componentState{"api": stateRunning}, stateStopped},
		{"one stopped", map[string]componentState{"api": stateRunning, "worker": stateStopped}, stateStopped},
		{"none", map[string]componentState{}, stateStopped},
	}
	for _, c := range cases {
		if got := composeComponentState(spec, c.services); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// A definition that declares no service cannot be judged either way.
	if got := composeComponentState(composeSpec{}, nil); got != stateUnknown {
		t.Errorf("no declared services = %q, want unknown", got)
	}
}
