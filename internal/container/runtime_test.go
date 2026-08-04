package container

// Tests for runtime.go: the capability report, plus the two lookups beside it.
// The point of the report is that an unusable provider is named *with the
// reason*, so most of these assertions are about what a host that has nothing
// installed is told. ComposeFor and DockerContextFor are about a different
// guarantee — that devhub follows the declaration rather than what the host
// turns out to be running.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func providerByID(providers []Provider, id string) Provider {
	for _, p := range providers {
		if p.ID == id {
			return p
		}
	}
	return Provider{}
}

func TestRuntimeProvidersReportsWhyEachIsUnusable(t *testing.T) {
	c := newTestRuntime(testDeps{
		compose: &fakeCompose{unavailable: ErrDockerMissing},
		colima:  &fakeColima{err: ErrColimaUnsupportedOS},
	})

	providers := c.Providers(context.Background())

	// The host provider needs nothing installed, so it stays available even on
	// a machine with no container tooling at all — that is what keeps a
	// host_process-only environment working (plan §11 PR 3).
	host := providerByID(providers, ProviderHost)
	if !host.Available || len(host.Engines) != 0 {
		t.Errorf("host = %+v, want available with no engines", host)
	}

	docker := providerByID(providers, ProviderDocker)
	if docker.Available || docker.Reason != ErrDockerMissing.Error() {
		t.Errorf("docker = %+v, want unavailable with the missing-binary reason", docker)
	}

	colima := providerByID(providers, ProviderColima)
	if colima.Available || colima.Reason != ErrColimaUnsupportedOS.Error() {
		t.Errorf("colima = %+v, want unavailable with the unsupported-OS reason", colima)
	}
	// Even unusable, the provider still advertises its engines: the UI renders
	// the engine choices from this list instead of hardcoding them (plan
	// §6.4). The list is what devhub can *drive*, not everything Colima can
	// host, so it never offers a choice devhub could not act on.
	if !slices.Equal(colima.Engines, drivableEngines()) {
		t.Errorf("colima engines = %v, want %v", colima.Engines, drivableEngines())
	}
}

// TestRuntimeProvidersSeparatesSupportedFromAvailable is what lets the UI hide
// Colima on Linux while still showing it — with the reason — on a macOS host
// that simply has not installed it (plan §10). Collapsing the two into one flag
// would force the UI to string-match the reason.
func TestRuntimeProvidersSeparatesSupportedFromAvailable(t *testing.T) {
	notInstalled := newTestRuntime(testDeps{
		colima: &fakeColima{err: ErrColimaMissing},
	})
	p := providerByID(notInstalled.Providers(context.Background()), ProviderColima)
	if p.Available || !p.Supported {
		t.Errorf("macOS without Colima = %+v, want unavailable but supported", p)
	}

	wrongOS := newTestRuntime(testDeps{
		colima: &fakeColima{err: ErrColimaUnsupportedOS},
	})
	p = providerByID(wrongOS.Providers(context.Background()), ProviderColima)
	if p.Available || p.Supported {
		t.Errorf("non-macOS = %+v, want neither available nor supported", p)
	}

	// The providers that need nothing installed are always both.
	for _, id := range []string{ProviderHost, ProviderDocker} {
		got := providerByID(wrongOS.Providers(context.Background()), id)
		if !got.Supported {
			t.Errorf("%s = %+v, want supported", id, got)
		}
	}
}

func TestRuntimeProvidersListsColimaProfiles(t *testing.T) {
	c := newTestRuntime(testDeps{
		colima: &fakeColima{profiles: []ColimaProfile{
			{Name: "default", Status: "Stopped", Arch: "aarch64"},
			{Name: "dev", Status: "Running", Engine: EngineDocker, Arch: "aarch64"},
		}},
	})

	colima := providerByID(c.Providers(context.Background()), ProviderColima)
	if !colima.Available || len(colima.Profiles) != 2 {
		t.Fatalf("colima = %+v, want available with 2 profiles", colima)
	}
	if got := colima.Profiles[0]; got.Context != "colima" || got.Engine != "" {
		t.Errorf("default profile = %+v, want context colima and an unknown engine", got)
	}
	if got := colima.Profiles[1]; got.Context != "colima-dev" || got.Engine != EngineDocker {
		t.Errorf("dev profile = %+v, want context colima-dev and the docker engine", got)
	}
	for _, p := range colima.Profiles {
		if !p.Supported || p.Reason != "" {
			t.Errorf("profile %s = %+v, want supported", p.Name, p)
		}
	}
}

// TestRuntimeProvidersFlagsUnsupportedEngines covers the engines Colima can
// host that devhub has no adapter for: such a profile must be listed with a
// reason, not offered as a choice devhub cannot act on. containerd belongs to
// this group until its adapter lands, which is why the assertion is written
// against drivableEngines rather than a hardcoded name.
func TestRuntimeProvidersFlagsUnsupportedEngines(t *testing.T) {
	c := newTestRuntime(testDeps{
		colima: &fakeColima{profiles: []ColimaProfile{
			{Name: "lxc", Status: "Running", Engine: "incus"},
			{Name: "ctr", Status: "Running", Engine: EngineContainerd},
			{Name: "dkr", Status: "Running", Engine: EngineDocker},
		}},
	})

	profiles := providerByID(c.Providers(context.Background()), ProviderColima).Profiles
	if len(profiles) != 3 {
		t.Fatalf("profiles = %+v", profiles)
	}
	for _, p := range profiles {
		want := slices.Contains(drivableEngines(), p.Engine)
		if p.Supported != want {
			t.Errorf("profile %s (engine %q) supported = %v, want %v", p.Name, p.Engine, p.Supported, want)
		}
		if !p.Supported && p.Reason == "" {
			t.Errorf("profile %s is unsupported without a reason", p.Name)
		}
	}
	// Colima's value is reported verbatim — "incus" is more useful to the user
	// than a blanked-out engine.
	if profiles[0].Engine != "incus" {
		t.Errorf("incus profile = %+v, want the engine reported as Colima named it", profiles[0])
	}
}

func TestDockerContextFor(t *testing.T) {
	cases := []struct {
		rt   Spec
		want string
	}{
		// The plain docker provider passes no --context: the ambient one is
		// the user's own choice, and naming it would freeze whatever it was
		// when the definition was written.
		{Spec{Provider: ProviderDocker}, ""},
		{Spec{Provider: ProviderHost}, ""},
		{Spec{Provider: ProviderColima}, "colima"},
		{Spec{Provider: ProviderColima, Profile: "dev"}, "colima-dev"},
	}
	for _, c := range cases {
		if got := DockerContextFor(c.rt); got != c.want {
			t.Errorf("DockerContextFor(%+v) = %q, want %q", c.rt, got, c.want)
		}
	}
}

// TestRuntimeWarnings covers what the user is told before devhub drives a
// Colima environment's containers. devhub never fixes any of it: starting or
// rebuilding a profile is destructive enough to stay the user's call.
func TestRuntimeWarnings(t *testing.T) {
	colimaEnv := func(profile, engine string) Spec {
		return Spec{Provider: ProviderColima, Profile: profile, Engine: engine}
	}
	warn := func(fake *fakeColima, rt Spec) []string {
		c := newTestRuntime(testDeps{colima: fake})
		return c.Warnings(context.Background(), rt)
	}
	contains := func(t *testing.T, got []string, want string) {
		t.Helper()
		for _, w := range got {
			if strings.Contains(w, want) {
				return
			}
		}
		t.Errorf("warnings = %v, want one containing %q", got, want)
	}

	// A docker environment must not pay for a Colima probe at all.
	probed := &fakeColima{}
	if w := warn(probed, Spec{Provider: ProviderDocker}); w != nil {
		t.Errorf("docker environment warned: %v", w)
	}
	if probed.calls != 0 {
		t.Errorf("probed Colima %d times for a docker environment", probed.calls)
	}

	running := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: EngineDocker}}}
	if w := warn(running, colimaEnv("dev", EngineDocker)); w != nil {
		t.Errorf("healthy profile warned: %v", w)
	}

	contains(t, warn(running, colimaEnv("ghost", "")), "見つかりません")

	stopped := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Stopped"}}}
	contains(t, warn(stopped, colimaEnv("dev", "")), "colima start -p dev")

	// A declared engine that disagrees with the running profile is reported,
	// never applied: switching a profile's engine would affect its existing
	// images and containers (plan §6.4).
	mismatch := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: EngineContainerd}}}
	contains(t, warn(mismatch, colimaEnv("dev", EngineDocker)), "engine を切り替えません")

	// An engine with no adapter at all is a different warning from a mismatch.
	incus := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: "incus"}}}
	contains(t, warn(incus, colimaEnv("dev", "")), "対応するアダプタがありません")

	// containerd is drivable but cannot report readiness, so an environment
	// that asks for it is told before the switch rather than after a dependent
	// component fails to connect.
	ctr := &fakeColima{profiles: []ColimaProfile{{Name: "dev", Status: "Running", Engine: EngineContainerd}}}
	got := warn(ctr, colimaEnv("dev", EngineContainerd))
	contains(t, got, "--wait")
	for _, w := range got {
		if strings.Contains(w, "engine を切り替えません") {
			t.Errorf("matching engines reported as a mismatch: %v", got)
		}
	}

	// A stopped profile reports no engine, so there is nothing to disagree
	// with — the "start it" warning is the whole story.
	quiet := warn(stopped, colimaEnv("dev", EngineDocker))
	for _, w := range quiet {
		if strings.Contains(w, "engine を切り替えません") {
			t.Errorf("unobservable engine reported as a mismatch: %v", quiet)
		}
	}

	// An environment naming no profile addresses colima's own default.
	deflt := &fakeColima{profiles: []ColimaProfile{{Name: "default", Status: "Running", Engine: EngineDocker}}}
	if w := warn(deflt, colimaEnv("", "")); w != nil {
		t.Errorf("default profile warned: %v", w)
	}

	broken := &fakeColima{err: ErrColimaMissing}
	contains(t, warn(broken, colimaEnv("dev", "")), ErrColimaMissing.Error())
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
	if _, err := c.ComposeFor(Spec{Provider: ProviderDocker, Engine: EngineContainerd}); !errors.Is(err, ErrContainerdUnsupported) {
		t.Errorf("err = %v, want ErrContainerdUnsupported", err)
	}
}

// blockingProbe and readyColima are local to the starvation test rather than
// living in fakes_test.go: only this test needs a probe that consumes a
// deadline, and only this test needs a Colima that consults the context.

// blockingProbe is an engine whose availability probe does not return until
// something else happens — a daemon that has just started, or a VM still
// booting. It unblocks as soon as the Colima probe has run, so the concurrent
// implementation finishes at once; only the sequential one, where nothing else
// can run first, pays the full deadline.
type blockingProbe struct{ colimaRan <-chan struct{} }

func (b blockingProbe) Available(ctx context.Context) error {
	select {
	case <-b.colimaRan:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (blockingProbe) ServiceStates(context.Context, Spec, ComposeSpec) (map[string]State, error) {
	return map[string]State{}, nil
}

func (blockingProbe) Up(context.Context, Spec, ComposeSpec) error   { return nil }
func (blockingProbe) Stop(context.Context, Spec, ComposeSpec) error { return nil }

// readyColima is a host that has Colima installed with a running profile. It
// consults the context, which fakeColima does not, because that is what the
// real colimaCLI does once past its darwin and lookPath checks: the answer
// comes from `colima list`, and an expired context fails it.
type readyColima struct {
	profiles []ColimaProfile
	// ran is closed on the first probe, which is what lets the docker double
	// stop waiting. Single-use, so a Once keeps a second call from panicking.
	ran  chan struct{}
	once sync.Once
}

func (c *readyColima) Profiles(ctx context.Context) ([]ColimaProfile, error) {
	c.once.Do(func() { close(c.ran) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.profiles, nil
}

// TestProvidersProbesDoNotStarveEachOther pins the concurrency in Providers.
// Run in sequence under a caller's deadline, a slow Docker probe spent all of
// it and Colima was then asked with an already-expired context — so a machine
// with a healthy Colima was reported unavailable and its profile list came
// back empty. That list is what the runtime picker is built from, so the
// symptom was a working execution base vanishing from the UI, not merely a
// worse error message.
//
// This is invisible to a response comparison against a previous build: both
// answer instantly when nothing is slow. It takes a probe that actually
// blocks, which is why the double is here.
func TestProvidersProbesDoNotStarveEachOther(t *testing.T) {
	lister := &readyColima{
		profiles: []ColimaProfile{{Name: "default", Status: "Running", Engine: "docker"}},
		ran:      make(chan struct{}),
	}
	r := &Runtime{
		Docker:     blockingProbe{colimaRan: lister.ran},
		Containerd: &fakeCompose{},
		Colima:     lister,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	colima := providerByID(r.Providers(ctx), ProviderColima)

	if !colima.Available {
		t.Fatalf("colima available = false (reason %q); the docker probe spent its budget", colima.Reason)
	}
	if len(colima.Profiles) != 1 {
		t.Errorf("profiles = %d, want 1 — this list is what the runtime picker offers", len(colima.Profiles))
	}
}
