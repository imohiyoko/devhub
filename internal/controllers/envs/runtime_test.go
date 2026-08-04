package envs

// Tests for the runtime capability report. The point of this endpoint is that
// an unusable provider is reported *with the reason*, so most of these
// assertions are about what a host that has nothing installed is told.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func providerByID(providers []RuntimeProvider, id string) RuntimeProvider {
	for _, p := range providers {
		if p.ID == id {
			return p
		}
	}
	return RuntimeProvider{}
}

func TestRuntimeProvidersReportsWhyEachIsUnusable(t *testing.T) {
	c, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{
		compose: &fakeCompose{unavailable: errDockerMissing},
		colima:  &fakeColima{err: errColimaUnsupportedOS},
	})

	providers := c.RuntimeProviders(context.Background())

	// The host provider needs nothing installed, so it stays available even on
	// a machine with no container tooling at all — that is what keeps a
	// host_process-only environment working (plan §11 PR 3).
	host := providerByID(providers, providerHost)
	if !host.Available || len(host.Engines) != 0 {
		t.Errorf("host = %+v, want available with no engines", host)
	}

	docker := providerByID(providers, providerDocker)
	if docker.Available || docker.Reason != errDockerMissing.Error() {
		t.Errorf("docker = %+v, want unavailable with the missing-binary reason", docker)
	}

	colima := providerByID(providers, providerColima)
	if colima.Available || colima.Reason != errColimaUnsupportedOS.Error() {
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
	notInstalled, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{
		colima: &fakeColima{err: errColimaMissing},
	})
	p := providerByID(notInstalled.RuntimeProviders(context.Background()), providerColima)
	if p.Available || !p.Supported {
		t.Errorf("macOS without Colima = %+v, want unavailable but supported", p)
	}

	wrongOS, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{
		colima: &fakeColima{err: errColimaUnsupportedOS},
	})
	p = providerByID(wrongOS.RuntimeProviders(context.Background()), providerColima)
	if p.Available || p.Supported {
		t.Errorf("non-macOS = %+v, want neither available nor supported", p)
	}

	// The providers that need nothing installed are always both.
	for _, id := range []string{providerHost, providerDocker} {
		got := providerByID(wrongOS.RuntimeProviders(context.Background()), id)
		if !got.Supported {
			t.Errorf("%s = %+v, want supported", id, got)
		}
	}
}

func TestRuntimeProvidersListsColimaProfiles(t *testing.T) {
	c, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{
		colima: &fakeColima{profiles: []colimaProfile{
			{Name: "default", Status: "Stopped", Arch: "aarch64"},
			{Name: "dev", Status: "Running", Engine: engineDocker, Arch: "aarch64"},
		}},
	})

	colima := providerByID(c.RuntimeProviders(context.Background()), providerColima)
	if !colima.Available || len(colima.Profiles) != 2 {
		t.Fatalf("colima = %+v, want available with 2 profiles", colima)
	}
	if got := colima.Profiles[0]; got.Context != "colima" || got.Engine != "" {
		t.Errorf("default profile = %+v, want context colima and an unknown engine", got)
	}
	if got := colima.Profiles[1]; got.Context != "colima-dev" || got.Engine != engineDocker {
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
	c, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{
		colima: &fakeColima{profiles: []colimaProfile{
			{Name: "lxc", Status: "Running", Engine: "incus"},
			{Name: "ctr", Status: "Running", Engine: engineContainerd},
			{Name: "dkr", Status: "Running", Engine: engineDocker},
		}},
	})

	profiles := providerByID(c.RuntimeProviders(context.Background()), providerColima).Profiles
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
		rt   runtimeSpec
		want string
	}{
		// The plain docker provider passes no --context: the ambient one is
		// the user's own choice, and naming it would freeze whatever it was
		// when the definition was written.
		{runtimeSpec{Provider: providerDocker}, ""},
		{runtimeSpec{Provider: providerHost}, ""},
		{runtimeSpec{Provider: providerColima}, "colima"},
		{runtimeSpec{Provider: providerColima, Profile: "dev"}, "colima-dev"},
	}
	for _, c := range cases {
		if got := dockerContextFor(c.rt); got != c.want {
			t.Errorf("dockerContextFor(%+v) = %q, want %q", c.rt, got, c.want)
		}
	}
}

// TestRuntimeWarnings covers what the user is told before devhub drives a
// Colima environment's containers. devhub never fixes any of it: starting or
// rebuilding a profile is destructive enough to stay the user's call.
func TestRuntimeWarnings(t *testing.T) {
	colimaEnv := func(profile, engine string) environment {
		return environment{ID: "e", Runtime: runtimeSpec{Provider: providerColima, Profile: profile, Engine: engine}}
	}
	warn := func(fake *fakeColima, env environment) []string {
		c, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{colima: fake})
		return c.RuntimeWarnings(context.Background(), env)
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
	if w := warn(probed, environment{ID: "e", Runtime: runtimeSpec{Provider: providerDocker}}); w != nil {
		t.Errorf("docker environment warned: %v", w)
	}
	if probed.calls != 0 {
		t.Errorf("probed Colima %d times for a docker environment", probed.calls)
	}

	running := &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Running", Engine: engineDocker}}}
	if w := warn(running, colimaEnv("dev", engineDocker)); w != nil {
		t.Errorf("healthy profile warned: %v", w)
	}

	contains(t, warn(running, colimaEnv("ghost", "")), "見つかりません")

	stopped := &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Stopped"}}}
	contains(t, warn(stopped, colimaEnv("dev", "")), "colima start -p dev")

	// A declared engine that disagrees with the running profile is reported,
	// never applied: switching a profile's engine would affect its existing
	// images and containers (plan §6.4).
	mismatch := &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Running", Engine: engineContainerd}}}
	contains(t, warn(mismatch, colimaEnv("dev", engineDocker)), "engine を切り替えません")

	// An engine with no adapter at all is a different warning from a mismatch.
	incus := &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Running", Engine: "incus"}}}
	contains(t, warn(incus, colimaEnv("dev", "")), "対応するアダプタがありません")

	// containerd is drivable but cannot report readiness, so an environment
	// that asks for it is told before the switch rather than after a dependent
	// component fails to connect.
	ctr := &fakeColima{profiles: []colimaProfile{{Name: "dev", Status: "Running", Engine: engineContainerd}}}
	got := warn(ctr, colimaEnv("dev", engineContainerd))
	contains(t, got, "--wait")
	for _, w := range got {
		if strings.Contains(w, "engine を切り替えません") {
			t.Errorf("matching engines reported as a mismatch: %v", got)
		}
	}

	// A stopped profile reports no engine, so there is nothing to disagree
	// with — the "start it" warning is the whole story.
	quiet := warn(stopped, colimaEnv("dev", engineDocker))
	for _, w := range quiet {
		if strings.Contains(w, "engine を切り替えません") {
			t.Errorf("unobservable engine reported as a mismatch: %v", quiet)
		}
	}

	// An environment naming no profile addresses colima's own default.
	deflt := &fakeColima{profiles: []colimaProfile{{Name: "default", Status: "Running", Engine: engineDocker}}}
	if w := warn(deflt, colimaEnv("", "")); w != nil {
		t.Errorf("default profile warned: %v", w)
	}

	broken := &fakeColima{err: errColimaMissing}
	contains(t, warn(broken, colimaEnv("dev", "")), errColimaMissing.Error())
}

func TestRuntimesEndpoint(t *testing.T) {
	c, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{
		compose: &fakeCompose{unavailable: errors.New("docker コマンドが見つかりません")},
		colima:  &fakeColima{err: errColimaMissing},
	})

	rec := httptest.NewRecorder()
	if err := c.HandleGet(rec, httptest.NewRequest(http.MethodGet, "/api/envs/runtimes", nil)); err != nil {
		t.Fatalf("HandleGet: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Providers []struct {
			ID        string `json:"id"`
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
			// Non-pointer slices with the JSON `null` value would decode as nil;
			// the endpoint must emit [] so the UI can iterate unconditionally.
			Engines  []string         `json:"engines"`
			Profiles []map[string]any `json:"profiles"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(body.Providers) != 3 {
		t.Fatalf("providers = %d, want host/docker/colima", len(body.Providers))
	}
	for _, p := range body.Providers {
		if p.Engines == nil || p.Profiles == nil {
			t.Errorf("provider %s serialised a null slice: %+v", p.ID, p)
		}
	}
	if p := body.Providers[2]; p.ID != providerColima || p.Available || p.Reason != errColimaMissing.Error() {
		t.Errorf("colima entry = %+v, want unavailable with the missing-CLI reason", p)
	}
}
