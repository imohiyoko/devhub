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
	runner := &fakeRunner{stdout: "", stderr: "panic: boom"}
	out, err := testControl(runner).Logs(context.Background(), dockerSrc(), realID, 10)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(out, "panic: boom") {
		t.Errorf("logs = %q, want the stderr stream", out)
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
// are often the same daemon, and the listing reports the containers under the
// profile. An operation naming the alias would otherwise look for them in a
// source that, by construction, lists none.
func TestResolveFollowsAnAlias(t *testing.T) {
	r := newTestRuntime(testDeps{compose: &fakeCompose{}})
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		"colima:dev": {{ID: realID, Name: "db"}},
	}}
	// Stand in for what Containers() would report: an alias and its target.
	r.Colima = &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: EngineDocker}}}

	got, err := r.ResolveContainer(context.Background(), "colima:dev", realID)
	if err != nil {
		t.Fatalf("ResolveContainer: %v", err)
	}
	if got.Source.ID != "colima:dev" || got.Container.Name != "db" {
		t.Errorf("resolved %+v under %s", got.Container, got.Source.ID)
	}
}
