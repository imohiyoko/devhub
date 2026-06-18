package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- test doubles ------------------------------------------------------------

// funcTool builds a Tool inline so tests don't depend on real controllers.
type funcTool struct {
	meta   Meta
	routes []Route
}

func (t funcTool) Meta() Meta      { return t.meta }
func (t funcTool) Routes() []Route { return t.routes }

// memStore is an in-memory Store for exercising the namespace seam.
type memStore struct{ m map[string][]byte }

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(k string) ([]byte, error) { return s.m[k], nil }
func (s *memStore) Set(k string, v []byte) error { s.m[k] = v; return nil }

func do(g *Gateway, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

// --- pillar 1: in-process dispatch via the plugin contract -------------------

func TestGateway_InProcDispatch(t *testing.T) {
	ping := funcTool{
		meta: Meta{ID: "ping", Title: "Ping"},
		routes: []Route{{
			Method: http.MethodGet, Pattern: "/api/ping/status",
			Handle: func(w http.ResponseWriter, _ *http.Request) error {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
				return nil
			},
		}},
	}
	g := NewGateway(NewRegistry(ping), nil, nil)

	w := do(g, http.MethodGet, "/api/ping/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != `{"ok":true}` {
		t.Fatalf("body = %q", got)
	}
}

// --- pillar 2: registry-driven nav (dashboard auto-generation) ---------------

func TestGateway_ToolsNav(t *testing.T) {
	reg := NewRegistry(
		funcTool{meta: Meta{ID: "ports", Title: "Ports"}},
		funcTool{meta: Meta{ID: "git", Title: "Git"}},
	)
	g := NewGateway(reg, nil, nil)

	w := do(g, http.MethodGet, "/api/tools")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Tools []Meta `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Metas() sorts by ID, so git precedes ports.
	if len(resp.Tools) != 2 || resp.Tools[0].ID != "git" || resp.Tools[1].ID != "ports" {
		t.Fatalf("nav = %+v", resp.Tools)
	}
}

// --- pillar 4: the gateway swaps a tool in-proc <-> reverse proxy ------------
// Same Tool, registered the same way. Whether it runs in-process or as its own
// service is decided purely by the Upstreams config: a tool can be "extracted"
// without a code change.

func TestGateway_ProxySwap(t *testing.T) {
	var inprocCalled bool
	git := funcTool{
		meta: Meta{ID: "git", Title: "Git"},
		routes: []Route{{
			Method: http.MethodGet, Pattern: "/api/git/", Prefix: true,
			Handle: func(w http.ResponseWriter, _ *http.Request) error {
				inprocCalled = true
				_, _ = w.Write([]byte("in-proc"))
				return nil
			},
		}},
	}

	// Default (no upstream): served in-process.
	g := NewGateway(NewRegistry(git), nil, nil)
	if w := do(g, http.MethodGet, "/api/git/status"); !inprocCalled || w.Body.String() != "in-proc" {
		t.Fatalf("expected in-proc handling; called=%v body=%q", inprocCalled, w.Body.String())
	}

	// A fake extracted "git service".
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("from-service"))
	}))
	defer upstream.Close()

	// Same registry, but config now points git at the service.
	inprocCalled = false
	gp := NewGateway(NewRegistry(git), Upstreams{"git": upstream.URL}, nil)

	w := do(gp, http.MethodGet, "/api/git/status")
	if inprocCalled {
		t.Fatal("in-proc handler must NOT run when the tool is extracted")
	}
	if gotPath != "/api/git/status" {
		t.Fatalf("upstream got %q, want /api/git/status", gotPath)
	}
	if w.Body.String() != "from-service" {
		t.Fatalf("body = %q, want from-service", w.Body.String())
	}
}

// --- in-process page serving via PageFunc ------------------------------------

func TestGateway_PageServe(t *testing.T) {
	diff := funcTool{meta: Meta{ID: "diff-kun", Title: "Diff", Page: "tools/diff-kun/index.html"}}
	pageFn := func(id string) ([]byte, bool) {
		if id == "diff-kun" {
			return []byte("<html>diff</html>"), true
		}
		return nil, false
	}
	g := NewGateway(NewRegistry(diff), nil, pageFn)

	for _, path := range []string{"/diff-kun", "/diff-kun/"} {
		w := do(g, http.MethodGet, path)
		if w.Code != http.StatusOK || w.Body.String() != "<html>diff</html>" {
			t.Fatalf("GET %s = %d %q", path, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("content-type = %q", ct)
		}
	}
}

func TestGateway_NotFound(t *testing.T) {
	g := NewGateway(NewRegistry(funcTool{meta: Meta{ID: "ping", Title: "Ping"}}), nil, nil)
	for _, path := range []string{"/api/nope", "/nope"} {
		if w := do(g, http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, w.Code)
		}
	}
}

// --- migration seam: unmatched requests fall through to Next ----------------

func TestGateway_NextFallthrough(t *testing.T) {
	ping := funcTool{
		meta: Meta{ID: "ping", Title: "Ping"},
		routes: []Route{{
			Method: http.MethodGet, Pattern: "/api/ping",
			Handle: func(w http.ResponseWriter, _ *http.Request) error {
				_, _ = w.Write([]byte("tool"))
				return nil
			},
		}},
	}
	g := NewGateway(NewRegistry(ping), nil, nil)

	var nextHits int
	g.Next = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextHits++
		_, _ = w.Write([]byte("legacy"))
	})

	// A claimed route is served by the tool; Next is not consulted.
	if w := do(g, http.MethodGet, "/api/ping"); w.Body.String() != "tool" || nextHits != 0 {
		t.Fatalf("claimed route: body=%q nextHits=%d", w.Body.String(), nextHits)
	}
	// An unclaimed route falls through to Next instead of 404.
	w := do(g, http.MethodGet, "/api/legacy/thing")
	if w.Body.String() != "legacy" || nextHits != 1 {
		t.Fatalf("fallthrough: body=%q nextHits=%d", w.Body.String(), nextHits)
	}
}

// --- pillar 1 (data ownership): per-tool storage namespaces never collide ----

func TestNamespace_Isolation(t *testing.T) {
	base := newMemStore()
	a := Namespace(base, "git")
	b := Namespace(base, "ports")

	if err := a.Set("config", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("config", []byte("B")); err != nil {
		t.Fatal(err)
	}

	av, _ := a.Get("config")
	bv, _ := b.Get("config")
	if string(av) != "A" || string(bv) != "B" {
		t.Fatalf("collision: a=%q b=%q", av, bv)
	}
	// Ownership is structural: keys are physically prefixed by tool ID.
	if _, ok := base.m["git:config"]; !ok {
		t.Fatalf("expected key git:config, have %v", keys(base.m))
	}
	if _, ok := base.m["ports:config"]; !ok {
		t.Fatalf("expected key ports:config, have %v", keys(base.m))
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Compile-time proof the test doubles satisfy the contracts.
var (
	_ Tool  = funcTool{}
	_ Store = (*memStore)(nil)
)
