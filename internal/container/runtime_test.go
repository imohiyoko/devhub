package container

// Tests for the runtime capability report. The point of this endpoint is that
// an unusable provider is reported *with the reason*, so most of these
// assertions are about what a host that has nothing installed is told.

import (
	"context"
	"slices"
	"strings"
	"testing"
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
		compose: &fakeCompose{unavailable: errDockerMissing},
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
	if docker.Available || docker.Reason != errDockerMissing.Error() {
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
