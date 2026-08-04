package container

// Tests for the one seam that moves a VM. Most of these are about what does
// *not* get spawned: every refusal here exists because the alternative takes
// containers down, and a refusal that happens after the stop is no refusal.

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func testAdmin(runner commandRunner, profiles ProfileLister) *colimaAdmin {
	return &colimaAdmin{
		runner:   runner,
		lookPath: func(string) (string, error) { return "/opt/homebrew/bin/colima", nil },
		darwin:   true,
		profiles: profiles,
	}
}

func noProfiles() *fakeColima { return &fakeColima{} }

func TestCreateArgv(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec ProfileSpec
		want []string
	}{
		{"sizes and engine", ProfileSpec{Name: "big", CPUs: 8, MemoryGiB: 16, DiskGiB: 200, Engine: EngineContainerd},
			[]string{"start", "--profile", "big", "--cpus", "8", "--memory", "16", "--disk", "200", "--runtime", "containerd"}},
		// Omitted sizes are omitted flags, not zeroes: colima's own defaults
		// apply, which is what "create a profile, I don't care how big" means.
		{"defaults", ProfileSpec{Name: "plain"},
			[]string{"start", "--profile", "plain"}},
		{"cpus only", ProfileSpec{Name: "cpu", CPUs: 4},
			[]string{"start", "--profile", "cpu", "--cpus", "4"}},
	} {
		runner := &fakeRunner{}
		if err := testAdmin(runner, noProfiles()).Create(context.Background(), tc.spec); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		call := runner.calls[0]
		if call.name != "colima" {
			t.Errorf("%s: ran %q", tc.name, call.name)
		}
		if !slices.Equal(call.args, tc.want) {
			t.Errorf("%s: args = %v\nwant %v", tc.name, call.args, tc.want)
		}
		if !call.bounded {
			t.Errorf("%s: spawned with no deadline", tc.name)
		}
	}
}

// TestCreateRefusesAnExistingName: a create that quietly became a resize would
// stop every container in that VM, and the caller asked for a new one.
func TestCreateRefusesAnExistingName(t *testing.T) {
	runner := &fakeRunner{}
	lister := &fakeColima{profiles: []ColimaProfile{{Name: "taken", Status: "Running"}}}
	err := testAdmin(runner, lister).Create(context.Background(), ProfileSpec{Name: "taken", CPUs: 4})
	if !errors.Is(err, ErrProfileExists) {
		t.Fatalf("err = %v, want ErrProfileExists", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("spawned %v after refusing", runner.calls)
	}
}

// TestResizeStopsFirstOnlyWhenRunning. Colima reads sizes at start, so a resize
// is stop-then-start — but a profile that is already down needs no stop, and
// issuing one anyway would report a failure for a VM that is in the wanted
// state.
func TestResizeStopsFirstOnlyWhenRunning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		want   [][]string
	}{
		{"running", "Running", [][]string{
			{"stop", "--profile", "dev"},
			{"start", "--profile", "dev", "--cpus", "8"},
		}},
		{"stopped", "Stopped", [][]string{
			{"start", "--profile", "dev", "--cpus", "8"},
		}},
	} {
		runner := &fakeRunner{}
		lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: tc.status, DiskBytes: 100 * gib}}}
		if err := testAdmin(runner, lister).Resize(context.Background(), ProfileSpec{Name: "dev", CPUs: 8}); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(runner.calls) != len(tc.want) {
			t.Errorf("%s: ran %d commands, want %d (%v)", tc.name, len(runner.calls), len(tc.want), runner.calls)
			continue
		}
		for i, want := range tc.want {
			if !slices.Equal(runner.calls[i].args, want) {
				t.Errorf("%s: call %d = %v\nwant %v", tc.name, i, runner.calls[i].args, want)
			}
		}
	}
}

// TestResizeRefusesADiskShrinkBeforeStopping is the safety property that
// matters most here. Shrinking recreates the VM and loses every image on it,
// and unlike a stop it cannot be undone by starting the profile again — so the
// refusal has to land before anything is taken down, not after.
func TestResizeRefusesADiskShrinkBeforeStopping(t *testing.T) {
	runner := &fakeRunner{}
	lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", DiskBytes: 200 * gib}}}

	err := testAdmin(runner, lister).Resize(context.Background(), ProfileSpec{Name: "dev", DiskGiB: 60})
	if !errors.Is(err, ErrDiskShrink) {
		t.Fatalf("err = %v, want ErrDiskShrink", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("ran %v before refusing; the VM was already down by then", runner.calls)
	}

	// Growing is fine, and so is leaving the disk alone.
	for _, spec := range []ProfileSpec{{Name: "dev", DiskGiB: 400}, {Name: "dev", CPUs: 2}} {
		runner := &fakeRunner{}
		if err := testAdmin(runner, lister).Resize(context.Background(), spec); err != nil {
			t.Errorf("%+v: %v", spec, err)
		}
	}
}

func TestResizeRefusesAMissingProfile(t *testing.T) {
	runner := &fakeRunner{}
	err := testAdmin(runner, noProfiles()).Resize(context.Background(), ProfileSpec{Name: "ghost", CPUs: 4})
	if !errors.Is(err, ErrProfileMissing) {
		t.Fatalf("err = %v, want ErrProfileMissing", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("spawned %v", runner.calls)
	}
}

// TestSpecIsCheckedBeforeAnythingSpawns: the name becomes an argv element and
// the engine decides whether devhub can drive the VM it is about to create.
// Both are rejected up front, so a bad request costs nothing and cannot leave a
// profile devhub has no adapter for.
func TestSpecIsCheckedBeforeAnythingSpawns(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec ProfileSpec
	}{
		{"empty name", ProfileSpec{}},
		{"space in name", ProfileSpec{Name: "my profile"}},
		{"flag-like name", ProfileSpec{Name: "--rm"}},
		{"path in name", ProfileSpec{Name: "../../etc"}},
		{"undrivable engine", ProfileSpec{Name: "ok", Engine: "incus"}},
		{"negative size", ProfileSpec{Name: "ok", CPUs: -1}},
	} {
		runner := &fakeRunner{}
		if err := testAdmin(runner, noProfiles()).Create(context.Background(), tc.spec); err == nil {
			t.Errorf("%s: accepted %+v", tc.name, tc.spec)
		}
		if len(runner.calls) != 0 {
			t.Errorf("%s: spawned %v", tc.name, runner.calls)
		}
	}
}

func TestProfileOpsRefuseNonDarwin(t *testing.T) {
	admin := testAdmin(&fakeRunner{}, noProfiles())
	admin.darwin = false
	if err := admin.Create(context.Background(), ProfileSpec{Name: "x"}); !errors.Is(err, ErrColimaUnsupportedOS) {
		t.Errorf("err = %v, want ErrColimaUnsupportedOS", err)
	}
}

// TestProfileTargetsNamesWhatAResizeStops is what makes the shared-profile case
// visible: two environments can name the same profile, and resizing for one
// takes down the other's containers.
func TestProfileTargetsNamesWhatAResizeStops(t *testing.T) {
	r := newTestRuntime(testDeps{colima: &fakeColima{profiles: []ColimaProfile{
		{Name: "shared", Status: "Running", Engine: EngineDocker},
		{Name: "idle", Status: "Stopped"},
	}}})
	r.Inventory = &fakeLister{bySource: map[string][]Container{
		"colima:shared": {{ID: "a", Name: "other-envs-db"}},
	}}

	got, err := r.ProfileTargets(context.Background(), "shared")
	if err != nil {
		t.Fatalf("ProfileTargets: %v", err)
	}
	if len(got) != 1 || got[0].Name != "other-envs-db" {
		t.Errorf("targets = %+v, want the running profile's containers", got)
	}

	// A stopped profile has nothing to take down, and must not be listed —
	// devhub does not reach into a VM that is not running.
	if got, err := r.ProfileTargets(context.Background(), "idle"); err != nil || len(got) != 0 {
		t.Errorf("stopped profile: %v, %v; want no targets and no error", got, err)
	}
	if _, err := r.ProfileTargets(context.Background(), "ghost"); !errors.Is(err, ErrProfileMissing) {
		t.Errorf("err = %v, want ErrProfileMissing", err)
	}
}
