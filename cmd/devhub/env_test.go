package main

import (
	"slices"
	"strings"
	"testing"
)

// TestParseSwitchArgs pins the switch argument grammar: an environment plus
// exactly one target, with -y anywhere. Requiring exactly one target is what
// stops `devhub env switch micro` from silently meaning "stop everything".
func TestParseSwitchArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantEnv    string
		wantScen   string
		wantStop   bool // target is the empty selection
		wantYes    bool
		wantErrHas string
	}{
		{name: "scenario", args: []string{"micro", "billing"}, wantEnv: "micro", wantScen: "billing"},
		{name: "scenario with -y", args: []string{"micro", "billing", "-y"}, wantEnv: "micro", wantScen: "billing", wantYes: true},
		{name: "--yes before the target", args: []string{"--yes", "micro", "billing"}, wantEnv: "micro", wantScen: "billing", wantYes: true},
		{name: "stop", args: []string{"micro", "--stop"}, wantEnv: "micro", wantStop: true},
		{name: "no args", args: nil, wantErrHas: "needs an environment id"},
		{name: "no target", args: []string{"micro"}, wantErrHas: "exactly one"},
		{name: "both targets", args: []string{"micro", "billing", "--stop"}, wantErrHas: "exactly one"},
	}
	for _, c := range cases {
		envID, target, yes, err := parseSwitchArgs(c.args)
		if c.wantErrHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErrHas) {
				t.Errorf("%s: err = %v, want containing %q", c.name, err, c.wantErrHas)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected err %v", c.name, err)
			continue
		}
		if envID != c.wantEnv || target.ScenarioID != c.wantScen || yes != c.wantYes {
			t.Errorf("%s: env=%q scenario=%q yes=%v", c.name, envID, target.ScenarioID, yes)
		}
		// --stop is an empty but non-nil selection; a scenario target leaves it nil.
		if gotStop := target.Components != nil && len(target.Components) == 0; gotStop != c.wantStop {
			t.Errorf("%s: components = %#v, want stop=%v", c.name, target.Components, c.wantStop)
		}
	}
}

// TestEnvSwitchUsageListsSubcommands keeps the help text honest about the
// subcommands runEnv actually dispatches.
func TestEnvSwitchUsageListsSubcommands(t *testing.T) {
	for _, sub := range []string{"list", "start", "stop", "status", "switch"} {
		if !strings.Contains(envUsage, "devhub env "+sub) {
			t.Errorf("env usage does not document %q", sub)
		}
	}
	if !strings.Contains(envUsage, "--stop") || !strings.Contains(envUsage, "-y") {
		t.Error("env usage must document --stop and -y")
	}
}

func TestRunEnvUnknownSubcommand(t *testing.T) {
	// runEnv opens the store, so point it at a throwaway home: a test must
	// never touch the developer's real ~/.devhub.
	t.Setenv("DEVHUB_HOME", t.TempDir())
	// A typo must not be mistaken for a switch target.
	if got := runEnv([]string{"swithc", "micro"}); got != 2 {
		t.Errorf("runEnv(swithc) = %d, want 2", got)
	}
	if got := runEnv([]string{"help"}); got != 0 {
		t.Errorf("runEnv(help) = %d, want 0", got)
	}
}

func TestParseSwitchArgsKeepsExtraPositionalOut(t *testing.T) {
	// A third positional is ignored rather than silently treated as a target,
	// so a mistyped flag cannot become the scenario.
	_, target, _, err := parseSwitchArgs([]string{"micro", "billing", "extra"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if target.ScenarioID != "billing" || target.Components != nil {
		t.Errorf("target = %+v", target)
	}
	if slices.Contains([]string{target.ScenarioID}, "extra") {
		t.Error("an extra positional must not become the target")
	}
}
