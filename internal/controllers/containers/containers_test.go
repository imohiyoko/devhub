package containers

// Tests for the inventory endpoint's payload. The listing itself is covered in
// internal/container; what matters here is the shape the panel receives, and
// the two decisions encoded in it: an unlistable source is still reported, and
// the rows arrive in an order the panel does not have to redo.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/imohiyoko/devhub/internal/container"
)

type fakeInventory struct {
	sources []container.Source
	list    []container.Container
}

func (f fakeInventory) Containers(context.Context) ([]container.Source, []container.Container) {
	return f.sources, f.list
}

func get(t *testing.T, inv fakeInventory) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	c := &Controller{runtime: inv}
	if err := c.HandleGet(rr, httptest.NewRequest(http.MethodGet, "/api/containers", nil)); err != nil {
		t.Fatalf("HandleGet: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rr.Body)
	}
	return out
}

// TestUnlistableSourcesReachThePanel: a stopped Colima profile is the case
// worth naming. Its containers exist on disk; devhub simply will not start the
// VM to see them (plan §13). Dropping the source would make that
// indistinguishable from a profile that owns nothing.
func TestUnlistableSourcesReachThePanel(t *testing.T) {
	out := get(t, fakeInventory{sources: []container.Source{
		{ID: "docker", Label: "Docker", Available: true},
		{ID: "colima:dev", Label: "Colima: dev", Profile: "dev",
			Reason: "profile 'dev' は Stopped です。`colima start -p dev` で起動してください。"},
	}})

	srcs, _ := out["sources"].([]any)
	if len(srcs) != 2 {
		t.Fatalf("sources = %v, want both", out["sources"])
	}
	stopped, _ := srcs[1].(map[string]any)
	if stopped["available"] != false {
		t.Errorf("available = %v, want false", stopped["available"])
	}
	if stopped["reason"] == "" {
		t.Error("the stopped profile arrived without a reason; that is the actionable part")
	}
}

// TestVMSizeIsOmittedWhenUnknown: absent rather than zero. A source with no VM
// behind it has no size, and "0 CPU" is a different claim from "not applicable"
// — one of which the panel would render as fact.
func TestVMSizeIsOmittedWhenUnknown(t *testing.T) {
	out := get(t, fakeInventory{sources: []container.Source{
		{ID: "docker", Label: "Docker", Available: true},
		{ID: "colima:dev", Label: "Colima: dev", Available: true,
			CPUs: 6, MemoryBytes: 17179869184, DiskBytes: 214748364800},
	}})
	srcs, _ := out["sources"].([]any)

	if plain, _ := srcs[0].(map[string]any); plain["cpus"] != nil || plain["memory_bytes"] != nil {
		t.Errorf("ambient docker reported a VM size: %v", plain)
	}
	vm, _ := srcs[1].(map[string]any)
	if vm["cpus"] != float64(6) || vm["memory_bytes"] != float64(17179869184) {
		t.Errorf("colima source size = %v/%v", vm["cpus"], vm["memory_bytes"])
	}
}

// TestContainerOrder pins the ordering the panel relies on: running first, then
// compose stacks grouped together, and containers no project owns last —
// because those are the rows that need a decision, and mixing them into a
// stack's services is how they stay unnoticed.
func TestContainerOrder(t *testing.T) {
	out := get(t, fakeInventory{
		sources: []container.Source{{ID: "docker", Label: "Docker", Available: true}},
		list: []container.Container{
			{ID: "1", Name: "zeta", State: "exited", Project: "beta", Service: "z"},
			{ID: "2", Name: "stray-live", State: "running"},
			{ID: "3", Name: "alpha-db", State: "running", Project: "alpha", Service: "db"},
			{ID: "4", Name: "", State: "exited"},
			{ID: "5", Name: "alpha-api", State: "running", Project: "alpha", Service: "api"},
		},
	})

	list, _ := out["containers"].([]any)
	got := make([]string, 0, len(list))
	for _, item := range list {
		m, _ := item.(map[string]any)
		got = append(got, m["display_name"].(string))
	}
	want := []string{"alpha-api", "alpha-db", "stray-live", "zeta", "4"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v\nwant     %v", got, want)
		}
	}

	// The nameless container is labelled by its ID rather than arriving blank,
	// and its true (empty) name is still reported.
	last, _ := list[4].(map[string]any)
	if last["name"] != "" || last["display_name"] != "4" {
		t.Errorf("nameless row: name=%q display_name=%q", last["name"], last["display_name"])
	}
}

// TestEmptyPayloadUsesArrays: the UI iterates without null checks, the same
// convention the runtimes endpoint follows.
func TestEmptyPayloadUsesArrays(t *testing.T) {
	out := get(t, fakeInventory{})
	for _, key := range []string{"sources", "containers"} {
		if _, ok := out[key].([]any); !ok {
			t.Errorf("%s = %#v, want an array", key, out[key])
		}
	}
}

// TestAliasReachesThePanel: the folded source keeps its entry and names where
// its rows went, so a section that suddenly held nothing cannot be mistaken for
// a broken listing.
func TestAliasReachesThePanel(t *testing.T) {
	out := get(t, fakeInventory{sources: []container.Source{
		{ID: "docker", Label: "Docker", Available: true, AliasOf: "colima:default"},
		{ID: "colima:default", Label: "Colima: default", Available: true},
	}})
	srcs, _ := out["sources"].([]any)
	folded, _ := srcs[0].(map[string]any)
	if folded["alias_of"] != "colima:default" {
		t.Errorf("alias_of = %v", folded["alias_of"])
	}
	kept, _ := srcs[1].(map[string]any)
	if kept["alias_of"] != "" {
		t.Errorf("the kept source reported alias_of = %v", kept["alias_of"])
	}
}
