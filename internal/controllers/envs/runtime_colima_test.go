package envs

// Tests for the Colima adapter. Nothing here runs Colima: the runner is a
// fake, so the assertions are about the argv devhub builds, how it reads
// `colima list --json`, and — most importantly — that it does not shell out at
// all on a host where Colima cannot exist.

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func testColima(runner commandRunner, darwin bool) *colimaCLI {
	return &colimaCLI{
		runner:   runner,
		lookPath: func(string) (string, error) { return "/opt/homebrew/bin/colima", nil },
		darwin:   darwin,
	}
}

// TestColimaProfilesSkipsNonDarwin is the plan §6.2 guarantee: on Linux and
// Windows devhub must not invoke colima at all, so the runner must stay
// untouched rather than merely have its failure handled.
func TestColimaProfilesSkipsNonDarwin(t *testing.T) {
	runner := &fakeRunner{}
	_, err := testColima(runner, false).Profiles(context.Background())
	if !errors.Is(err, errColimaUnsupportedOS) {
		t.Fatalf("err = %v, want errColimaUnsupportedOS", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("ran %v on a non-macOS host; nothing should be spawned", runner.calls)
	}
}

func TestColimaProfilesMissingCLI(t *testing.T) {
	runner := &fakeRunner{}
	adapter := testColima(runner, true)
	adapter.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	_, err := adapter.Profiles(context.Background())
	if !errors.Is(err, errColimaMissing) {
		t.Fatalf("err = %v, want errColimaMissing", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("ran %v without a colima binary", runner.calls)
	}
}

func TestColimaProfilesArgvAndParsing(t *testing.T) {
	// Real `colima list --json` output (0.10.1): one JSON object per line, and
	// a stopped profile carries no "runtime" key at all.
	runner := &fakeRunner{stdout: `{"name":"default","status":"Stopped","arch":"aarch64","cpus":6}
{"name":"dev","status":"Running","arch":"aarch64","runtime":"docker"}`}

	profiles, err := testColima(runner, true).Profiles(context.Background())
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "colima" || call.cwd != "" {
		t.Errorf("ran %q in %q, want colima with no cwd", call.name, call.cwd)
	}
	if got, want := call.args, []string{"list", "--json"}; !slices.Equal(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}

	if len(profiles) != 2 {
		t.Fatalf("profiles = %+v, want 2", profiles)
	}
	// A stopped VM reports no engine, and devhub must leave it unknown rather
	// than assume the default (plan §6.4).
	if profiles[0].Name != "default" || profiles[0].Engine != "" || profiles[0].running() {
		t.Errorf("stopped profile = %+v, want name default, no engine, not running", profiles[0])
	}
	if profiles[1].Engine != engineDocker || !profiles[1].running() {
		t.Errorf("running profile = %+v, want docker engine and running", profiles[1])
	}
}

func TestParseColimaList(t *testing.T) {
	// No profile at all: colima 0.10.1 prints nothing and exits zero, which is
	// an empty list rather than a failure.
	profiles, err := parseColimaList("\n  \n")
	if err != nil || len(profiles) != 0 {
		t.Errorf("empty output: profiles = %+v, err = %v, want none", profiles, err)
	}

	if _, err := parseColimaList("{not json}"); err == nil {
		t.Error("malformed output accepted; a parse failure must surface as the reason")
	}
}

func TestColimaProfilesSurfacesCLIError(t *testing.T) {
	runner := &fakeRunner{stderr: "FATA[0000] error listing instances\nsecond line", err: errors.New("exit status 1")}

	_, err := testColima(runner, true).Profiles(context.Background())
	if err == nil || err.Error() != "FATA[0000] error listing instances" {
		t.Errorf("err = %v, want colima's own first stderr line", err)
	}
}

// TestColimaDockerContext pins the profile→context mapping. It is not a plain
// "colima-"+name: getting it wrong would point --context at a VM that does not
// exist, and the failure would look like "the daemon is down".
func TestColimaDockerContext(t *testing.T) {
	cases := map[string]string{
		"":              "colima",
		"default":       "colima",
		"colima":        "colima",
		"dev":           "colima-dev",
		"colima-shared": "colima-shared",
	}
	for profile, want := range cases {
		if got := colimaDockerContext(profile); got != want {
			t.Errorf("colimaDockerContext(%q) = %q, want %q", profile, got, want)
		}
	}
}
