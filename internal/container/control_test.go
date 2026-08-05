package container

// Tests for the seam that stops containers. As in profile_test.go, most of what
// is asserted is that nothing ran: every refusal here exists because the
// alternative acts on a container the caller could not have been looking at.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

const realID = "abc123def456"

func testControl(runner commandRunner) *cliControl { return &cliControl{runner: runner} }

func dockerSrc() Source { return Source{ID: "docker", Engine: EngineDocker} }

func containerdSrc() Source {
	return Source{ID: "colima:dev", Engine: EngineContainerd, Profile: "dev"}
}

func TestControlArgv(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  Source
		call func(*cliControl) error
		want []string
	}{
		{"docker stop", dockerSrc(),
			func(c *cliControl) error { return c.Stop(context.Background(), dockerSrc(), realID) },
			[]string{"stop", realID}},
		{"docker start", dockerSrc(),
			func(c *cliControl) error { return c.Start(context.Background(), dockerSrc(), realID) },
			[]string{"start", realID}},
		{"docker restart", dockerSrc(),
			func(c *cliControl) error { return c.Restart(context.Background(), dockerSrc(), realID) },
			[]string{"restart", realID}},
		{"docker logs", dockerSrc(),
			func(c *cliControl) error {
				_, err := c.Logs(context.Background(), dockerSrc(), realID, 50)
				return err
			},
			[]string{"logs", "--tail", "50", realID}},
		{"colima context", Source{ID: "x", Engine: EngineDocker, Context: "colima-dev"},
			func(c *cliControl) error {
				return c.Stop(context.Background(), Source{ID: "x", Engine: EngineDocker, Context: "colima-dev"}, realID)
			},
			[]string{"--context", "colima-dev", "stop", realID}},
		// containerd is reached through colima's passthrough, with `--` keeping
		// colima's flags apart from nerdctl's.
		{"containerd stop", containerdSrc(),
			func(c *cliControl) error { return c.Stop(context.Background(), containerdSrc(), realID) },
			[]string{"nerdctl", "--profile", "dev", "--", "stop", realID}},
		{"containerd start", containerdSrc(),
			func(c *cliControl) error { return c.Start(context.Background(), containerdSrc(), realID) },
			[]string{"nerdctl", "--profile", "dev", "--", "start", realID}},
	} {
		runner := &fakeRunner{}
		if err := tc.call(testControl(runner)); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		call := runner.calls[0]
		wantBin := "docker"
		if tc.src.Engine == EngineContainerd {
			wantBin = "colima"
		}
		if call.name != wantBin {
			t.Errorf("%s: ran %q, want %q", tc.name, call.name, wantBin)
		}
		if !slices.Equal(call.args, tc.want) {
			t.Errorf("%s: args = %v\nwant %v", tc.name, call.args, tc.want)
		}
		if !call.bounded {
			t.Errorf("%s: spawned with no deadline", tc.name)
		}
	}
}

// TestLogTailIsClamped: a tail is a number in a request that becomes a number
// in an argv, and the answer travels through a JSON response into a browser.
func TestLogTailIsClamped(t *testing.T) {
	for _, tc := range []struct {
		asked int
		want  string
	}{
		{0, "200"},        // absent means the default
		{-5, "200"},       // so does nonsense
		{50, "50"},        // a reasonable ask is honoured
		{1000000, "2000"}, // an unreasonable one is capped, not refused
	} {
		runner := &fakeRunner{}
		if _, err := testControl(runner).Logs(context.Background(), dockerSrc(), realID, tc.asked); err != nil {
			t.Fatalf("%d: %v", tc.asked, err)
		}
		args := runner.calls[0].args
		if got := args[slices.Index(args, "--tail")+1]; got != tc.want {
			t.Errorf("tail %d -> %s, want %s", tc.asked, got, tc.want)
		}
	}
}

// TestLogsCarryStderr. A container that writes only to stderr would otherwise
// come back empty, and "no logs" is a different statement from "logs on the
// other stream".
func TestLogsCarryStderr(t *testing.T) {
	for _, tc := range []struct{ stdout, stderr string }{
		{"", "panic: boom"},               // stderr only
		{"listening on :8080", "warn: x"}, // both, and neither may be dropped
	} {
		runner := &fakeRunner{stdout: tc.stdout, stderr: tc.stderr}
		out, err := testControl(runner).Logs(context.Background(), dockerSrc(), realID, 10)
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		if tc.stdout != "" && !strings.Contains(out, tc.stdout) {
			t.Errorf("logs = %q, missing stdout", out)
		}
		if !strings.Contains(out, tc.stderr) {
			t.Errorf("logs = %q, missing stderr", out)
		}
	}
}

// TestControlSurfacesTheCLIsWords. "No such container" and "permission denied"
// are things devhub cannot say better than the engine did, and a caller
// deciding what to do next needs the specific one.
func TestControlSurfacesTheCLIsWords(t *testing.T) {
	runner := &fakeRunner{err: errors.New("exit status 1"), stderr: "Error: No such container: abc"}
	c := testControl(runner)
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"stop", func() error { return c.Stop(context.Background(), dockerSrc(), realID) }},
		{"restart", func() error { return c.Restart(context.Background(), dockerSrc(), realID) }},
		{"logs", func() error {
			_, err := c.Logs(context.Background(), dockerSrc(), realID, 10)
			return err
		}},
	} {
		err := tc.call()
		if err == nil || !strings.Contains(err.Error(), "No such container") {
			t.Errorf("%s: err = %v, want the CLI's own stderr", tc.name, err)
		}
	}
}

// TestResolveRefusesWhatItCannotSee is the bound the whole Surface rests on. An
// ID that is not in the engine's own listing never becomes an argv element, so
// a caller cannot reach a container by guessing at one.
func TestResolveRefusesWhatItCannotSee(t *testing.T) {
	r := newTestRuntime(testDeps{
		compose: &fakeCompose{},
		colima:  &fakeColima{err: ErrColimaMissing},
	})
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		"docker": {{ID: realID, Name: "db"}},
	}}

	got, err := r.ResolveContainer(context.Background(), "docker", realID)
	if err != nil {
		t.Fatalf("ResolveContainer: %v", err)
	}
	if got.Container.Name != "db" {
		t.Errorf("resolved %+v, want the listed container", got.Container)
	}

	for _, tc := range []struct {
		name     string
		source   string
		id       string
		wantErr  error
		badShape bool
	}{
		// Well-formed but absent: the engine is not reporting it.
		{"unlisted id", "docker", "ffffffffffff", ErrContainerMissing, false},
		{"unknown source", "colima:ghost", realID, ErrSourceMissing, false},
		// Shapes that must not reach a listing, let alone a command line.
		{"flag-like", "docker", "--rm", ErrContainerMissing, true},
		{"path", "docker", "../../etc/passwd", ErrContainerMissing, true},
		{"name not id", "docker", "db", ErrContainerMissing, true},
		{"empty", "docker", "", ErrContainerMissing, true},
		{"too short", "docker", "abc", ErrContainerMissing, true},
	} {
		_, err := r.ResolveContainer(context.Background(), tc.source, tc.id)
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
		}
		if tc.badShape && ValidContainerID(tc.id) {
			t.Errorf("%s: %q passed ValidContainerID", tc.name, tc.id)
		}
	}
}

// TestResolveFollowsAnAlias. The ambient Docker context and a Colima profile
// are usually the same daemon, so the listing reports each container once,
// under the profile. An operation naming the ambient source has to end up
// there — otherwise the most common arrangement on a Mac is the one that does
// not work.
//
// Both sources must list the same container for collapseAliases to see the
// overlap and mark the alias at all; an earlier version of this test put the
// container under one source only, so no alias was ever created and the path
// it claimed to cover ran zero times.
func TestResolveFollowsAnAlias(t *testing.T) {
	r := aliasedRuntime()

	sources, _ := r.Containers(context.Background())
	ambient, _ := findSource(sources, ProviderDocker)
	if ambient.AliasOf == "" {
		t.Fatalf("no alias was created, so this test would prove nothing: %+v", sources)
	}

	// Named by the alias, answered by the source that owns the rows.
	got, err := r.ResolveContainer(context.Background(), ProviderDocker, realID)
	if err != nil {
		t.Fatalf("ResolveContainer: %v", err)
	}
	if got.Source.ID != "colima:dev" || got.Container.Name != "db" {
		t.Errorf("resolved %+v under %q, want the owning source", got.Container, got.Source.ID)
	}
}

// aliasedRuntime is the common Mac arrangement: one Colima profile, and an
// ambient Docker context pointing at that same VM, so both listings return the
// same container.
func aliasedRuntime() *Runtime {
	r := newTestRuntime(testDeps{
		compose: &fakeCompose{},
		colima:  &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: EngineDocker}}},
	})
	same := []Container{{ID: realID, Name: "db"}}
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		ProviderDocker: same,
		"colima:dev":   same,
	}}
	return r
}

// TestResolveRefusesAnUnlistableSource: a source whose listing failed is
// reported with its reason rather than as "no such container", because those
// two call for different things from the user.
func TestResolveRefusesAnUnlistableSource(t *testing.T) {
	r := newTestRuntime(testDeps{compose: &fakeCompose{}})
	r.Inventory = &fakeLister{err: map[string]error{
		ProviderDocker: errors.New("Cannot connect to the Docker daemon"),
	}}

	_, err := r.ResolveContainer(context.Background(), ProviderDocker, realID)
	if !errors.Is(err, ErrSourceMissing) {
		t.Fatalf("err = %v, want ErrSourceMissing", err)
	}
	// The engine's own words survive: "start Docker" and "that container is
	// gone" are not the same instruction.
	if !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("err = %q, want the CLI's reason", err)
	}
}

// TestResolveAcceptsEitherIDForm. `docker ps` prints twelve hex digits and
// `--no-trunc` prints sixty-four; an agent holding the long form is naming the
// same container the panel shows.
func TestResolveAcceptsEitherIDForm(t *testing.T) {
	full := realID + strings.Repeat("0", 64-len(realID))
	r := newTestRuntime(testDeps{compose: &fakeCompose{}})
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		ProviderDocker: {{ID: full, Name: "db"}},
	}}

	for _, id := range []string{full, realID, strings.ToUpper(realID)} {
		got, err := r.ResolveContainer(context.Background(), ProviderDocker, id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		// Whatever was asked, the argv gets the ID the engine reported.
		if got.Container.ID != full {
			t.Errorf("%s: resolved to %q, want the listed ID", id, got.Container.ID)
		}
	}

	// An ambiguous prefix is refused rather than resolved to whichever was
	// listed first.
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		ProviderDocker: {{ID: full, Name: "db"}, {ID: realID + "ffffffffffff", Name: "cache"}},
	}}
	if _, err := r.ResolveContainer(context.Background(), ProviderDocker, realID); !errors.Is(err, ErrContainerMissing) {
		t.Errorf("err = %v, want a refusal for an ambiguous prefix", err)
	}
}
