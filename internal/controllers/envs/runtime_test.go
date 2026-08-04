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
	// Even unusable, the provider still advertises which engines it *could*
	// run: the UI renders the engine choices from this list instead of
	// hardcoding them (plan §6.4).
	if len(colima.Engines) != 2 {
		t.Errorf("colima engines = %v, want docker and containerd", colima.Engines)
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
