package container

// Tests for the containerd adapter. Its whole reason to exist is that the
// argv differs from Docker's, so that is what these assert — plus the two
// places the engines genuinely diverge: no --wait on up, and a Colima profile
// instead of a Docker context.

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func testNerdctl(runner commandRunner) *nerdctlCompose {
	return &nerdctlCompose{
		runner:   runner,
		lookPath: func(string) (string, error) { return "/opt/homebrew/bin/colima", nil },
		darwin:   true,
	}
}

// TestNerdctlArgv pins the passthrough shape. The `--` separator is load
// bearing: colima's own -p selects the VM profile and compose's -p names the
// project, so without it the project name would be read as a profile.
func TestNerdctlArgv(t *testing.T) {
	spec := ComposeSpec{Cwd: "~/platform", Files: []string{"compose.yml"},
		Project: "platform-local", Services: []string{"mysql", "redis"}}

	runner := &fakeRunner{stdout: "[]"}
	if _, err := testNerdctl(runner).ServiceStates(context.Background(), containerdRT("dev"), spec); err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	call := runner.calls[0]
	if call.name != "colima" {
		t.Errorf("ran %q, want colima", call.name)
	}
	want := []string{"nerdctl", "--profile", "dev", "--", "compose", "--project-name", "platform-local",
		"--file", "compose.yml", "ps", "--format", "json", "--all"}
	if got := call.args; !slices.Equal(got, want) {
		t.Errorf("args = %v\nwant %v", got, want)
	}
	// The compose file path is expanded on the host; it resolves inside the VM
	// only because Colima mounts the home directory at the same path.
	if call.args[7] == "~/platform/compose.yml" {
		t.Error("compose file path was not expanded")
	}

	// up carries no --wait: nerdctl has no such flag, and pretending otherwise
	// would make the command fail outright.
	up := &fakeRunner{}
	if err := testNerdctl(up).Up(context.Background(), containerdRT("dev"), spec); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if slices.Contains(up.calls[0].args, "--wait") {
		t.Errorf("up args = %v, want no --wait", up.calls[0].args)
	}
	wantUp := []string{"nerdctl", "--profile", "dev", "--", "compose", "--project-name", "platform-local",
		"--file", "compose.yml", "up", "--detach", "mysql", "redis"}
	if got := up.calls[0].args; !slices.Equal(got, wantUp) {
		t.Errorf("up args = %v\nwant %v", got, wantUp)
	}

	stop := &fakeRunner{}
	if err := testNerdctl(stop).Stop(context.Background(), containerdRT("dev"), spec); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	wantStop := []string{"nerdctl", "--profile", "dev", "--", "compose", "--project-name", "platform-local",
		"--file", "compose.yml", "stop", "mysql", "redis"}
	if got := stop.calls[0].args; !slices.Equal(got, wantStop) {
		t.Errorf("stop args = %v\nwant %v", got, wantStop)
	}
}

// TestNerdctlAddressesTheDefaultProfile covers an environment that names the
// provider but no profile: it must still land on a real VM.
func TestNerdctlAddressesTheDefaultProfile(t *testing.T) {
	runner := &fakeRunner{stdout: "[]"}
	rt := Spec{Provider: ProviderColima, Engine: EngineContainerd}
	if _, err := testNerdctl(runner).ServiceStates(context.Background(), rt, ComposeSpec{Project: "p"}); err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	if got := runner.calls[0].args[2]; got != DefaultColimaProfile {
		t.Errorf("profile = %q, want %q", got, DefaultColimaProfile)
	}
}

func TestNerdctlAvailability(t *testing.T) {
	// Colima is the gate: nerdctl lives inside the profile's VM, so there is
	// no host-side CLI to look for.
	runner := &fakeRunner{}
	if err := testNerdctl(runner).Available(context.Background()); err != nil {
		t.Errorf("Available: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("availability spawned %v; it must answer without entering a VM", runner.calls)
	}

	missing := testNerdctl(&fakeRunner{})
	missing.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if err := missing.Available(context.Background()); !errors.Is(err, ErrColimaMissing) {
		t.Errorf("err = %v, want ErrColimaMissing", err)
	}

	linux := testNerdctl(&fakeRunner{})
	linux.darwin = false
	if err := linux.Available(context.Background()); !errors.Is(err, ErrColimaUnsupportedOS) {
		t.Errorf("err = %v, want ErrColimaUnsupportedOS", err)
	}
	// An operation on a host that cannot run Colima must fail without
	// spawning anything.
	runner = &fakeRunner{}
	linux.runner = runner
	if err := linux.Up(context.Background(), containerdRT("dev"), ComposeSpec{Project: "p"}); err == nil {
		t.Error("Up succeeded on a non-macOS host")
	}
	if len(runner.calls) != 0 {
		t.Errorf("spawned %v on a non-macOS host", runner.calls)
	}
}

func TestNerdctlSurfacesCLIError(t *testing.T) {
	runner := &fakeRunner{
		stderr: `time="…" level=fatal msg="colima is not running"` + "\nsecond line",
		err:    errors.New("exit status 1"),
	}
	_, err := testNerdctl(runner).ServiceStates(context.Background(), containerdRT("dev"), ComposeSpec{Project: "p"})
	if err == nil || err.Error() != `time="…" level=fatal msg="colima is not running"` {
		t.Errorf("err = %v, want colima's own first stderr line", err)
	}
}

// TestNerdctlPSParsing covers nerdctl's own JSON: the field names match
// Docker's, but a stopped container reports the raw containerd status
// "exited" rather than Docker's wording.
func TestNerdctlPSParsing(t *testing.T) {
	runner := &fakeRunner{stdout: `{"ID":"a","Name":"p_mysql_1","Project":"p","Service":"mysql","State":"running","Health":"","ExitCode":0}
{"ID":"b","Name":"p_redis_1","Project":"p","Service":"redis","State":"exited","Health":"","ExitCode":1}`}

	states, err := testNerdctl(runner).ServiceStates(context.Background(), containerdRT("dev"), ComposeSpec{Project: "p"})
	if err != nil {
		t.Fatalf("ServiceStates: %v", err)
	}
	if states["mysql"] != StateRunning || states["redis"] != StateStopped {
		t.Errorf("states = %v, want mysql running and redis stopped", states)
	}
}

// TestComposeForPicksTheAdapter covers engine selection. It follows the
// declaration, not the profile's reality: devhub never silently re-routes to
// another engine (plan §6.4).
func TestComposeForPicksTheAdapter(t *testing.T) {
	c := newTestRuntime(testDeps{})
	c.Containerd = &fakeCompose{}

	for _, tc := range []struct {
		name string
		rt   Spec
		want Adapter
	}{
		{"docker provider", Spec{Provider: ProviderDocker}, c.Docker},
		{"colima without an engine", Spec{Provider: ProviderColima}, c.Docker},
		{"colima with docker", Spec{Provider: ProviderColima, Engine: EngineDocker}, c.Docker},
		{"colima with containerd", Spec{Provider: ProviderColima, Engine: EngineContainerd}, c.Containerd},
	} {
		got, err := c.ComposeFor(tc.rt)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: picked the wrong adapter", tc.name)
		}
	}

	// containerd outside Colima is rejected rather than driven with Docker.
	// Save-time validation already refuses it; decode is lenient, so a
	// hand-edited document can still reach here.
	if _, err := c.ComposeFor(Spec{Provider: ProviderDocker, Engine: EngineContainerd}); !errors.Is(err, errContainerdUnsupported) {
		t.Errorf("err = %v, want errContainerdUnsupported", err)
	}
}

func containerdRT(profile string) Spec {
	return Spec{Provider: ProviderColima, Profile: profile, Engine: EngineContainerd}
}
