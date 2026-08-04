package container

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
	"time"
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
	// bounded and budget record the deadline the command ran under. Since the
	// adapters set their own deadlines and every caller now passes a plain
	// background context, this is the only place a test can see that a bound
	// was applied at all, and which one.
	bounded bool
	budget  time.Duration
}

func (f *fakeRunner) Run(ctx context.Context, cwd, name string, args ...string) (string, string, error) {
	call := runnerCall{cwd: cwd, name: name, args: args}
	if deadline, ok := ctx.Deadline(); ok {
		call.bounded, call.budget = true, time.Until(deadline).Round(time.Second)
	}
	f.calls = append(f.calls, call)
	return f.stdout, f.stderr, f.err
}

// colimaRT is an environment running on a named Colima profile.
func colimaRT(profile string) Spec {
	return Spec{Provider: ProviderColima, Profile: profile}
}

func testCompose(runner commandRunner) *dockerCompose {
	return &dockerCompose{
		runner:   runner,
		lookPath: func(string) (string, error) { return "/usr/local/bin/docker", nil },
	}
}

// TestComposeAvailableChecksThePlugin covers the gap between "docker is
// installed" and "docker compose works": they are separate packages on most
// Linux distributions, and the capability API must not promise the second
// because it found the first.
func TestComposeAvailableChecksThePlugin(t *testing.T) {
	runner := &fakeRunner{stdout: "v2.39.4\n"}
	adapter := testCompose(runner)
	if err := adapter.Available(context.Background()); err != nil {
		t.Fatalf("Available: %v", err)
	}
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0].args, []string{"compose", "version", "--short"}) {
		t.Errorf("probe = %+v, want a bare `compose version --short`", runner.calls)
	}

	// Docker present, Compose plugin absent: Docker's own wording is what
	// tells the user which half is missing. (The message below is what Docker
	// CLI 29 prints for an unknown subcommand; older builds say "is not a
	// docker command" — devhub passes through whichever it gets.)
	missingPlugin := &fakeRunner{
		stderr: "docker: unknown command: docker compose",
		err:    errors.New("exit status 1"),
	}
	err := testCompose(missingPlugin).Available(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown command: docker compose") {
		t.Errorf("err = %v, want Docker's missing-plugin message", err)
	}

	noBinary := testCompose(&fakeRunner{})
	noBinary.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := noBinary.Available(context.Background()); !errors.Is(err, ErrDockerMissing) {
		t.Errorf("err = %v, want ErrDockerMissing", err)
	}
}

// TestComposeRunSkipsThePluginProbe keeps the operational path cheap: every
// compose call would otherwise pay for a second process, and the command it is
// about to run reports a missing plugin by itself.
func TestComposeRunSkipsThePluginProbe(t *testing.T) {
	runner := &fakeRunner{stdout: "[]"}
	if _, err := testCompose(runner).ServiceStates(context.Background(), Spec{}, ComposeSpec{Project: "p"}); err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Errorf("calls = %+v, want only the ps invocation", runner.calls)
	}
}

// TestComposeArgvCarriesTheDockerContext pins where --context goes. It is a
// flag of `docker` itself, so it must precede the `compose` subcommand:
// `docker compose --context …` is rejected as an unknown flag (verified
// against Docker CLI 29.4.0). Nothing here runs `docker context use` — the
// context is named per invocation so other terminals are unaffected (plan
// §6.3).
func TestComposeArgvCarriesTheDockerContext(t *testing.T) {
	spec := ComposeSpec{Cwd: "~/platform", Project: "platform-local", Services: []string{"mysql", "redis"}}

	runner := &fakeRunner{stdout: "[]"}
	if _, err := testCompose(runner).ServiceStates(context.Background(), colimaRT("dev"), spec); err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	want := []string{"--context", "colima-dev", "compose", "--project-name", "platform-local", "ps", "--format", "json", "--all"}
	if got := runner.calls[0].args; !slices.Equal(got, want) {
		t.Errorf("args = %v\nwant %v", got, want)
	}

	up := &fakeRunner{}
	if err := testCompose(up).Up(context.Background(), colimaRT("dev"), spec); err != nil {
		t.Fatalf("Up: %v", err)
	}
	wantUp := []string{"--context", "colima-dev", "compose", "--project-name", "platform-local", "up", "--detach", "--wait", "mysql", "redis"}
	if got := up.calls[0].args; !slices.Equal(got, wantUp) {
		t.Errorf("up args = %v\nwant %v", got, wantUp)
	}

	stop := &fakeRunner{}
	if err := testCompose(stop).Stop(context.Background(), colimaRT("dev"), spec); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := stop.calls[0].args; !slices.Equal(got[:2], []string{"--context", "colima-dev"}) {
		t.Errorf("stop args = %v, want --context first", got)
	}

	// An empty context passes no flag at all, so the ambient context stays in
	// charge and a user who switched contexts in their shell gets what they
	// expect.
	ambient := &fakeRunner{stdout: "[]"}
	if _, err := testCompose(ambient).ServiceStates(context.Background(), Spec{}, spec); err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	if got := ambient.calls[0].args[0]; got != "compose" {
		t.Errorf("args start with %q, want the bare compose subcommand", got)
	}
}

func TestComposeServiceStatesBuildsScopedArgv(t *testing.T) {
	runner := &fakeRunner{stdout: `[{"Service":"mysql","State":"running"}]`}
	spec := ComposeSpec{Cwd: "~/platform", Files: []string{"compose.yml", "compose.override.yml"},
		Project: "platform-local", Services: []string{"mysql"}}

	states, err := testCompose(runner).ServiceStates(context.Background(), Spec{}, spec)
	if err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	if states["mysql"] != StateRunning {
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
	_, err := adapter.ServiceStates(context.Background(), Spec{}, ComposeSpec{Project: "p"})
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
	_, err = testCompose(runner).ServiceStates(context.Background(), Spec{}, ComposeSpec{Project: "p"})
	if err == nil || !strings.Contains(err.Error(), "failed to connect to the docker API") {
		t.Errorf("daemon down: err = %v, want docker's own message", err)
	}
	if err != nil && strings.Contains(err.Error(), "second line") {
		t.Errorf("only the first stderr line should surface, got %q", err)
	}

	// A failure with nothing on stderr still reports something actionable.
	runner = &fakeRunner{err: errors.New("context deadline exceeded")}
	_, err = testCompose(runner).ServiceStates(context.Background(), Spec{}, ComposeSpec{Project: "p"})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("silent failure: err = %v", err)
	}
}

func TestParseComposePS(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want map[string]State
	}{
		{"empty output", "", map[string]State{}},
		{"json array", `[{"Service":"a","State":"running"},{"Service":"b","State":"exited"}]`,
			map[string]State{"a": StateRunning, "b": StateStopped}},
		{"newline delimited", "{\"Service\":\"a\",\"State\":\"running\"}\n{\"Service\":\"b\",\"State\":\"created\"}\n",
			map[string]State{"a": StateRunning, "b": StateStopped}},
		{"empty array", "[]", map[string]State{}},
		// Replicas: one container down makes the whole service not running,
		// whichever order the entries arrive in.
		{"replica down after up", `[{"Service":"a","State":"running"},{"Service":"a","State":"exited"}]`,
			map[string]State{"a": StateStopped}},
		{"replica down before up", `[{"Service":"a","State":"exited"},{"Service":"a","State":"running"}]`,
			map[string]State{"a": StateStopped}},
		{"entry without a service name is skipped", `[{"State":"running"}]`, map[string]State{}},
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
	spec := ComposeSpec{Services: []string{"api", "worker"}}
	cases := []struct {
		name     string
		services map[string]State
		want     State
	}{
		{"all running", map[string]State{"api": StateRunning, "worker": StateRunning}, StateRunning},
		{"one missing", map[string]State{"api": StateRunning}, StateStopped},
		{"one stopped", map[string]State{"api": StateRunning, "worker": StateStopped}, StateStopped},
		{"none", map[string]State{}, StateStopped},
	}
	for _, c := range cases {
		if got := ComposeState(spec, c.services); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// A definition that declares no service cannot be judged either way.
	if got := ComposeState(ComposeSpec{}, nil); got != StateUnknown {
		t.Errorf("no declared services = %q, want unknown", got)
	}
}
