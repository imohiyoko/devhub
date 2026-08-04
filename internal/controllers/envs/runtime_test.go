package envs

// The env-launcher side of the runtime surface: the capability report rendered
// for its own API. What that report says is the container package's business
// and is tested there; what matters here is the JSON the UI receives.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/imohiyoko/devhub/internal/container"
)

func TestRuntimesEndpoint(t *testing.T) {
	c, _ := newTestController(&fakeStore{envs: map[string]any{}}, testDeps{
		compose: &fakeCompose{unavailable: container.ErrDockerMissing},
		colima:  &fakeColima{err: container.ErrColimaMissing},
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
	if p := body.Providers[2]; p.ID != container.ProviderColima || p.Available || p.Reason != container.ErrColimaMissing.Error() {
		t.Errorf("colima entry = %+v, want unavailable with the missing-CLI reason", p)
	}
}
