package container

// Tests for the one seam that moves a VM. Most of these are about what does
// *not* get spawned: every refusal here exists because the alternative takes
// containers down, and a refusal that happens after the stop is no refusal.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imohiyoko/devhub/internal/hostspec"
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

// cappedAdmin is testAdmin plus a host to measure against: ten cores and
// 32 GiB, the machine this was written on. With the default 20% reserve that
// works out to a cap of 8 CPU and 25 GiB.
//
// testAdmin itself leaves host nil on purpose, so every test written before the
// cap existed goes on exercising the undetected-host path — which is what a
// non-darwin build and a failed sysctl both look like, and it must stay a
// straight passthrough.
func cappedAdmin(runner commandRunner, profiles ProfileLister, reserve Reserve) *colimaAdmin {
	a := testAdmin(runner, profiles)
	a.host = func() hostspec.Spec {
		return hostspec.Spec{CPUs: 10, MemoryBytes: 32 * gibDivisor, Detected: true}
	}
	a.reserve = func() Reserve { return reserve }
	return a
}

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

// TestResizeRefusesAnEngineChangeBeforeStopping. Unlike a disk shrink this one
// looks harmless — the VM stops and comes back, nothing is deleted — but the
// containers live in the runtime's own store, so after the swap they simply
// stop being there as far as anything asking can tell. That is the one outcome
// the confirmation cannot describe, because the stop list names what goes down
// and these do not go down.
func TestResizeRefusesAnEngineChangeBeforeStopping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		asked   string
		refuse  bool
	}{
		{"docker to containerd", EngineDocker, EngineContainerd, true},
		// A stopped profile reports no engine, so devhub cannot tell a change
		// from a restatement — and guessing wrong is the swap itself.
		{"current unknown", "", EngineContainerd, true},
		// Restating the engine it already has asks for nothing.
		{"same engine", EngineDocker, EngineDocker, false},
		{"omitted", EngineDocker, "", false},
	} {
		lister := &fakeColima{profiles: []ColimaProfile{
			{Name: "dev", Status: "Running", Engine: tc.current, DiskBytes: 100 * gib},
		}}
		runner := &fakeRunner{}
		err := testAdmin(runner, lister).Resize(context.Background(), ProfileSpec{Name: "dev", CPUs: 2, Engine: tc.asked})

		if !tc.refuse {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, ErrEngineChange) {
			t.Errorf("%s: err = %v, want ErrEngineChange", tc.name, err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("%s: ran %v before refusing; the VM was already down by then", tc.name, runner.calls)
		}
		// The dry run refuses it too, so the user is never asked to agree to a
		// resize that cannot happen.
		if err := testAdmin(&fakeRunner{}, lister).CheckResize(
			context.Background(), ProfileSpec{Name: "dev", Engine: tc.asked}); !errors.Is(err, ErrEngineChange) {
			t.Errorf("%s: CheckResize = %v, want ErrEngineChange", tc.name, err)
		}
	}
}

// TestResizeSaysHowToRecoverWhenStartFails. This is the one error where the
// user ends up with less than they had: the stop ran, so the VM is down and the
// new size did not take. colima's own message says why it would not start; it
// does not say that nothing is running now.
func TestResizeSaysHowToRecoverWhenStartFails(t *testing.T) {
	runner := &failOn{verb: "start", err: errors.New("boom")}
	lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", DiskBytes: 100 * gib}}}

	err := testAdmin(runner, lister).Resize(context.Background(), ProfileSpec{Name: "dev", CPUs: 2})
	if err == nil {
		t.Fatal("start failed but Resize did not")
	}
	for _, want := range []string{"停止しています", "colima start --profile dev", "2 CPU"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err, want)
		}
	}
}

// failOn fails one colima subcommand and lets the rest through.
type failOn struct {
	verb string
	err  error
}

func (f *failOn) Run(_ context.Context, _, _ string, args ...string) (string, string, error) {
	if len(args) > 0 && args[0] == f.verb {
		return "", "", f.err
	}
	return "", "", nil
}

// TestConcurrentCreatesOfOneNameStartItOnce. "Does this profile exist" is only
// worth as much as the window after it: two tabs, or an agent and a browser,
// both get "no" and both run colima against the same VM.
func TestConcurrentCreatesOfOneNameStartItOnce(t *testing.T) {
	lister := &growingColima{}
	runner := &startRecorder{lister: lister}
	admin := testAdmin(runner, lister)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = admin.Create(context.Background(), ProfileSpec{Name: "race", CPUs: 1})
		}()
	}
	wg.Wait()

	if got := runner.started(); got != 1 {
		t.Errorf("ran colima start %d times for one profile", got)
	}
	// One caller made it; the other is told it already exists. Neither is told
	// something that did not happen.
	var made, existed int
	for _, err := range errs {
		switch {
		case err == nil:
			made++
		case errors.Is(err, ErrProfileExists):
			existed++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if made != 1 || existed != 1 {
		t.Errorf("created=%d, already-exists=%d; want one of each", made, existed)
	}
}

// growingColima is a listing that reflects what has been started so far, which
// is what makes the race above visible: with a static one, the second caller
// would find nothing whether or not the lock worked.
type growingColima struct {
	mu       sync.Mutex
	profiles []ColimaProfile
}

func (g *growingColima) Profiles(context.Context) ([]ColimaProfile, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.profiles), nil
}

// startRecorder makes `colima start` take long enough that a second request
// arrives while the first is still running, then registers the profile.
type startRecorder struct {
	lister *growingColima
	mu     sync.Mutex
	starts int
}

func (s *startRecorder) Run(_ context.Context, _, _ string, args ...string) (string, string, error) {
	if len(args) < 3 || args[0] != "start" {
		return "", "", nil
	}
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	s.starts++
	s.mu.Unlock()
	s.lister.mu.Lock()
	s.lister.profiles = append(s.lister.profiles, ColimaProfile{Name: args[2], Status: "Running"})
	s.lister.mu.Unlock()
	return "", "", nil
}

func (s *startRecorder) started() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

// TestResizeRefusesADiskChangeItCannotJudge. Colima does report a stopped
// profile's disk, so this is narrow — but among everything refused here only a
// shrink is unrecoverable, and a guard that opens when the current value is
// missing is open in the wrong direction. The engine check already refuses on
// "cannot tell"; this matches it.
func TestResizeRefusesADiskChangeItCannotJudge(t *testing.T) {
	runner := &fakeRunner{}
	lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running"}}} // DiskBytes unset

	err := testAdmin(runner, lister).Resize(context.Background(), ProfileSpec{Name: "dev", DiskGiB: 400})
	if !errors.Is(err, ErrDiskShrink) {
		t.Fatalf("err = %v, want ErrDiskShrink", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("ran %v; the VM was already down by the time this was known", runner.calls)
	}
	// Sizes that are not the disk are unaffected — an unknown disk is only a
	// reason to refuse a disk change.
	if err := testAdmin(&fakeRunner{}, lister).Resize(
		context.Background(), ProfileSpec{Name: "dev", CPUs: 2}); err != nil {
		t.Errorf("CPU-only resize refused: %v", err)
	}
}

// TestSizesTooLargeAreRefusedBeforeStopping. A resize colima rejects has already
// had its stop run, so a number that could never have worked has to be turned
// away first. This does not catch every rejection colima would make — devhub
// does not know the host — only the ones it could know without asking.
func TestSizesTooLargeAreRefusedBeforeStopping(t *testing.T) {
	for _, spec := range []ProfileSpec{
		{Name: "dev", CPUs: 100000000},
		{Name: "dev", MemoryGiB: 1 << 40},
		{Name: "dev", DiskGiB: 1 << 40},
		// Past ~8.6e9 GiB the byte count overflows int64 and goes negative, so
		// the shrink check would refuse it for a reason that is not the truth.
		{Name: "dev", DiskGiB: 9_000_000_000},
	} {
		runner := &fakeRunner{}
		lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", DiskBytes: 100 * gib}}}

		err := testAdmin(runner, lister).Resize(context.Background(), spec)
		if err == nil {
			t.Errorf("%+v: accepted", spec)
		}
		if errors.Is(err, ErrDiskShrink) {
			t.Errorf("%+v: refused as a shrink (%v), which is not why", spec, err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("%+v: ran %v before refusing", spec, runner.calls)
		}
		// Create has nothing to stop, but the same numbers are still nonsense.
		if err := testAdmin(&fakeRunner{}, noProfiles()).Create(context.Background(), spec); err == nil {
			t.Errorf("%+v: create accepted", spec)
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

// TestStartArgv: a start passes no size flags at all. That is what separates it
// from a resize — colima keeps each profile's configuration, so an omitted flag
// means "whatever this profile already is", and a start that sent sizes would be
// a resize without the confirmation a resize is required to have.
func TestStartArgv(t *testing.T) {
	runner := &fakeRunner{}
	lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Stopped"}}}
	if err := testAdmin(runner, lister).Start(context.Background(), "dev"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %v, want one", runner.calls)
	}
	call := runner.calls[0]
	want := []string{"start", "--profile", "dev"}
	if call.name != "colima" || !slices.Equal(call.args, want) {
		t.Errorf("ran %q %v\nwant colima %v", call.name, call.args, want)
	}
	if !call.bounded {
		t.Error("spawned with no deadline")
	}
}

func TestStopArgv(t *testing.T) {
	runner := &fakeRunner{}
	lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running"}}}
	if err := testAdmin(runner, lister).Stop(context.Background(), "dev"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %v, want one", runner.calls)
	}
	call := runner.calls[0]
	want := []string{"stop", "--profile", "dev"}
	if call.name != "colima" || !slices.Equal(call.args, want) {
		t.Errorf("ran %q %v\nwant colima %v", call.name, call.args, want)
	}
	// --force skips the guest's shutdown. The whole point of showing the user
	// what will stop is that those containers get to exit cleanly.
	if slices.Contains(call.args, "--force") {
		t.Error("passed --force; the guest would not shut down cleanly")
	}
	if !call.bounded {
		t.Error("spawned with no deadline")
	}
}

// TestStartAndStopAreIdempotent: both are read as "make it so". A profile that
// is already in the wanted state is left alone rather than cycled, so a second
// click cannot turn a satisfied request into a stop nobody asked for.
func TestStartAndStopAreIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		op     func(*colimaAdmin) error
	}{
		{"start a running profile", "Running", func(a *colimaAdmin) error {
			return a.Start(context.Background(), "dev")
		}},
		{"stop a stopped profile", "Stopped", func(a *colimaAdmin) error {
			return a.Stop(context.Background(), "dev")
		}},
	} {
		runner := &fakeRunner{}
		lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: tc.status}}}
		if err := tc.op(testAdmin(runner, lister)); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("%s: spawned %v for a VM already in that state", tc.name, runner.calls)
		}
	}
}

// TestStartAndStopRefuseAnUnknownProfile. Start refuses rather than creating:
// Create is the door for a VM that does not exist and it takes a size, so
// starting a free name would make a default-sized VM out of a typo.
func TestStartAndStopRefuseAnUnknownProfile(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   func(*colimaAdmin) error
	}{
		{"start", func(a *colimaAdmin) error { return a.Start(context.Background(), "ghost") }},
		{"stop", func(a *colimaAdmin) error { return a.Stop(context.Background(), "ghost") }},
	} {
		runner := &fakeRunner{}
		err := tc.op(testAdmin(runner, noProfiles()))
		if !errors.Is(err, ErrProfileMissing) {
			t.Errorf("%s: err = %v, want ErrProfileMissing", tc.name, err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("%s: spawned %v after refusing", tc.name, runner.calls)
		}
	}
}

// TestStartAndStopRefuseABadName: the name becomes an argv element, and it is
// checked before anything is spawned or even looked up.
func TestStartAndStopRefuseABadName(t *testing.T) {
	for _, name := range []string{"", "--rm", "-f", "my profile", "../etc"} {
		for _, tc := range []struct {
			verb string
			op   func(*colimaAdmin, string) error
		}{
			{"start", func(a *colimaAdmin, n string) error { return a.Start(context.Background(), n) }},
			{"stop", func(a *colimaAdmin, n string) error { return a.Stop(context.Background(), n) }},
		} {
			runner := &fakeRunner{}
			lister := &fakeColima{profiles: []ColimaProfile{{Name: name, Status: "Running"}}}
			if err := tc.op(testAdmin(runner, lister), name); err == nil {
				t.Errorf("%s %q: accepted", tc.verb, name)
			}
			if len(runner.calls) != 0 {
				t.Errorf("%s %q: reached a command line", tc.verb, name)
			}
		}
	}
}

// --- ホスト容量の上限 -------------------------------------------------------

// TestCapRefusesBeforeSpawning: a size this machine cannot back is turned away
// without a command line. That is the whole point — colima would have refused
// it too, but on a resize colima's refusal arrives after the stop.
func TestCapRefusesBeforeSpawning(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   func(*colimaAdmin) error
	}{
		{"create cpus", func(a *colimaAdmin) error {
			return a.Create(context.Background(), ProfileSpec{Name: "big", CPUs: 16})
		}},
		{"create memory", func(a *colimaAdmin) error {
			return a.Create(context.Background(), ProfileSpec{Name: "big", MemoryGiB: 64})
		}},
		{"resize", func(a *colimaAdmin) error {
			return a.Resize(context.Background(), ProfileSpec{Name: "dev", MemoryGiB: 64})
		}},
		// The dry run must refuse too, or the user agrees to stop a VM full of
		// containers for an operation that was never going to run.
		{"check resize", func(a *colimaAdmin) error {
			return a.CheckResize(context.Background(), ProfileSpec{Name: "dev", CPUs: 16})
		}},
	} {
		runner := &fakeRunner{}
		lister := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running"}}}
		err := tc.op(cappedAdmin(runner, lister, DefaultReserve()))

		if !errors.Is(err, ErrOverHostCapacity) {
			t.Errorf("%s: err = %v, want ErrOverHostCapacity", tc.name, err)
		}
		if len(runner.calls) != 0 {
			t.Errorf("%s: spawned %v — the VM was touched for a size that could never work",
				tc.name, runner.calls)
		}
	}
}

// TestCapMessageNamesTheWayOut. Raising the reserve can put an existing VM out
// of reach of the panel, so a refusal that does not say how to undo itself
// leaves the user with a machine they can see and cannot start.
func TestCapMessageNamesTheWayOut(t *testing.T) {
	err := cappedAdmin(&fakeRunner{}, noProfiles(), DefaultReserve()).
		Create(context.Background(), ProfileSpec{Name: "big", CPUs: 16, MemoryGiB: 64})
	if err == nil {
		t.Fatal("accepted")
	}
	msg := err.Error()
	for _, want := range []string{
		"CPU 16", "上限 8", "実装 10", // what was asked, what is allowed, what exists
		"メモリ 64 GiB", "上限 25 GiB", "実装 32 GiB",
		"20%",          // the reserve, in the form it was written
		"予約を減らす",       // way out #1
		"colima start", // way out #2
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
}

// TestCapUsesTheReserveAsWritten: an absolute reserve is reported as one. A
// user who set "4 cores" must not be told about a percentage devhub computed,
// because they would go looking for a setting they never touched.
func TestCapUsesTheReserveAsWritten(t *testing.T) {
	res := Reserve{CPU: Amount{Absolute: 4, Set: true}, Memory: Amount{Absolute: 8, Set: true}}
	err := cappedAdmin(&fakeRunner{}, noProfiles(), res).
		Create(context.Background(), ProfileSpec{Name: "big", CPUs: 7})
	if err == nil {
		t.Fatal("accepted 7 CPUs against a cap of 6")
	}
	if !strings.Contains(err.Error(), "予約 4コア") {
		t.Errorf("message did not name the reserve as written:\n%s", err)
	}
}

// TestCapAllowsWhatFits, including the boundary: a cap of 8 permits 8.
func TestCapAllowsWhatFits(t *testing.T) {
	runner := &fakeRunner{}
	err := cappedAdmin(runner, noProfiles(), DefaultReserve()).
		Create(context.Background(), ProfileSpec{Name: "fits", CPUs: 8, MemoryGiB: 25})
	if err != nil {
		t.Fatalf("refused a size that fits exactly: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Errorf("calls = %v, want the create to have run", runner.calls)
	}
}

// TestStartRefusesAProfileThatNoLongerFits is the check asked for by name: the
// size judged is the profile's own, since a start carries none.
func TestStartRefusesAProfileThatNoLongerFits(t *testing.T) {
	runner := &fakeRunner{}
	lister := &fakeColima{profiles: []ColimaProfile{
		{Name: "hog", Status: "Stopped", CPUs: 16, MemoryBytes: 64 * gibDivisor},
	}}
	err := cappedAdmin(runner, lister, DefaultReserve()).Start(context.Background(), "hog")

	if !errors.Is(err, ErrOverHostCapacity) {
		t.Fatalf("err = %v, want ErrOverHostCapacity", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("spawned %v", runner.calls)
	}
}

// TestOverSizedVMsCanStillBeStopped. A cap that blocked the stop would be the
// worst failure available: the user could not reclaim the very memory the cap
// exists to protect. This is why the check is not in check(), which Stop shares.
func TestOverSizedVMsCanStillBeStopped(t *testing.T) {
	runner := &fakeRunner{}
	lister := &fakeColima{profiles: []ColimaProfile{
		{Name: "hog", Status: "Running", CPUs: 16, MemoryBytes: 64 * gibDivisor},
	}}
	if err := cappedAdmin(runner, lister, DefaultReserve()).Stop(context.Background(), "hog"); err != nil {
		t.Fatalf("could not stop an over-sized VM: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %v, want the stop to have run", runner.calls)
	}
	// And a VM that is already up stays reachable: the "already running" answer
	// comes before the cap, so raising the reserve cannot make a live VM look
	// like something devhub must refuse.
	runner2 := &fakeRunner{}
	if err := cappedAdmin(runner2, lister, DefaultReserve()).Start(context.Background(), "hog"); err != nil {
		t.Errorf("a running over-sized VM was refused: %v", err)
	}
}

// TestNoHostNoCap: on a machine devhub cannot measure — every non-darwin build,
// or a failed sysctl — the absolute limits are all that remain, and nothing new
// is refused.
func TestNoHostNoCap(t *testing.T) {
	runner := &fakeRunner{}
	// testAdmin, not cappedAdmin: host is nil.
	if err := testAdmin(runner, noProfiles()).
		Create(context.Background(), ProfileSpec{Name: "big", CPUs: 512, MemoryGiB: 999}); err != nil {
		t.Fatalf("refused without a host to justify it: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Errorf("calls = %v, want the create to have run", runner.calls)
	}
}

// TestBudgetTotalsTheRunningVMs. The sum can exceed the cap and often will —
// the cap bounds one VM, and nothing stops two from being up — which is exactly
// why a caller wants the figure.
func TestBudgetTotalsTheRunningVMs(t *testing.T) {
	lister := &fakeColima{profiles: []ColimaProfile{
		{Name: "a", Status: "Running", CPUs: 8, MemoryBytes: 20 * gibDivisor},
		{Name: "b", Status: "Running", CPUs: 6, MemoryBytes: 16 * gibDivisor},
		{Name: "c", Status: "Stopped", CPUs: 4, MemoryBytes: 8 * gibDivisor},
	}}
	b, err := cappedAdmin(&fakeRunner{}, lister, DefaultReserve()).Budget(context.Background())
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if !b.Detected || b.CPUCap != 8 || b.MemCapGiB != 25 {
		t.Errorf("budget = %+v, want the caps for a 10-core / 32 GiB host", b)
	}
	// A stopped profile is allocated nothing: it is holding no memory.
	if b.RunningCPUs != 14 || b.RunningMemGiB != 36 {
		t.Errorf("running = %d CPU / %d GiB, want 14 / 36", b.RunningCPUs, b.RunningMemGiB)
	}
}

// TestBudgetSurvivesAColimaFailure: the host half comes from syscalls, not from
// colima, and it is the half the caps are made of. A panel that lost the limit
// because colima was busy would stop showing a rule that is still in force.
func TestBudgetSurvivesAColimaFailure(t *testing.T) {
	lister := &fakeColima{err: ErrColimaMissing}
	b, err := cappedAdmin(&fakeRunner{}, lister, DefaultReserve()).Budget(context.Background())
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if !b.Detected || b.CPUCap != 8 {
		t.Errorf("budget = %+v, want the host half intact", b)
	}
	if b.RunningCPUs != 0 {
		t.Errorf("running = %d, want 0 when colima could not be asked", b.RunningCPUs)
	}
}
