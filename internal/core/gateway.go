package core

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/imohiyoko/devhub/internal/httpx"
)

// Upstreams maps a tool ID to a base URL (e.g. "http://127.0.0.1:9001"). When an
// entry is present, the gateway reverse-proxies that tool's page (/<id>) and API
// (/api/<id>/) to the URL instead of serving it in-process.
//
// This is how a tool is "extracted" to its own process: add a config entry and
// run the same Tool behind its own binary. Nothing else changes — the contract
// (routes, JSON shapes) is identical in-proc and over the wire. Default: empty,
// so everything runs in the single binary.
type Upstreams map[string]string

// PageFunc renders a tool's page bytes (e.g. embedded HTML with the API-token
// shim injected). It returns false when the tool has no servable page. The
// gateway stays decoupled from the embed/asset layer this way.
type PageFunc func(toolID string) ([]byte, bool)

// Gateway is the HTTP front door. It dispatches each request to an in-process
// tool handler or to an upstream tool service, and serves GET /api/tools for
// the dashboard nav. It is derived entirely from the Registry + Upstreams, so
// adding or extracting a tool never edits this file.
type Gateway struct {
	metas   []Meta
	proxies []proxyMount      // extracted tools
	routes  []inprocRoute     // in-process API endpoints
	pages   map[string]string // request path -> tool ID, for in-process pages
	pageFn  PageFunc

	// Next handles requests no tool claimed. During the migration this is the
	// legacy router, so not-yet-migrated paths keep working; once every tool is
	// migrated it can be nil and the Gateway answers 404 itself.
	Next http.Handler
}

type proxyMount struct {
	proxy    http.Handler
	patterns []routeMatch // the page + API patterns this tool owns
}

type routeMatch struct {
	pattern string
	prefix  bool
}

func (pm proxyMount) matches(path string) bool {
	for _, m := range pm.patterns {
		if m.pattern == path || (m.prefix && strings.HasPrefix(path, m.pattern)) {
			return true
		}
	}
	return false
}

type inprocRoute struct {
	method  string
	pattern string
	prefix  bool
	handle  Handler
}

// NewGateway wires a registry into an http.Handler. up selects which tools run
// out-of-process; pageFn supplies page bytes for in-process tools (may be nil
// for API-only setups).
func NewGateway(reg *Registry, up Upstreams, pageFn PageFunc) *Gateway {
	g := &Gateway{pages: map[string]string{}, pageFn: pageFn}

	// Nav (/api/tools) lists only tools with a page; API-only tools (e.g.
	// settings) have no dashboard card. reg.Metas() is already sorted by ID.
	for _, m := range reg.Metas() {
		if m.Page != "" {
			g.metas = append(g.metas, m)
		}
	}

	for _, t := range reg.Tools() {
		m := t.Meta()
		if base := up[m.ID]; base != "" {
			if pm, err := newProxyMount(t, base); err == nil {
				g.proxies = append(g.proxies, pm)
				continue // extracted: do not mount its in-process page/routes
			}
			// A bad upstream URL must not make the tool vanish: fall through and
			// mount it in-process so it stays reachable in the single binary.
		}
		if m.Page != "" {
			g.pages["/"+m.ID] = m.ID
			g.pages["/"+m.ID+"/"] = m.ID
		}
		for _, rt := range t.Routes() {
			g.routes = append(g.routes, inprocRoute{rt.Method, rt.Pattern, rt.Prefix, rt.Handle})
		}
	}
	// Longest pattern first so a specific exact route wins over a broader prefix.
	sort.SliceStable(g.routes, func(i, j int) bool {
		return len(g.routes[i].pattern) > len(g.routes[j].pattern)
	})
	return g
}

// newProxyMount captures the tool's own page and API patterns so an extracted
// tool is matched by exactly the routes it declares — not a guessed /api/<id>/,
// which would be wrong for tools whose API prefix differs from their ID (e.g.
// db-table → /api/db/, env-launcher → /api/envs).
func newProxyMount(t Tool, base string) (proxyMount, error) {
	u, err := url.Parse(base)
	if err != nil {
		return proxyMount{}, err
	}
	pm := proxyMount{proxy: httputil.NewSingleHostReverseProxy(u)}
	if m := t.Meta(); m.Page != "" {
		pm.patterns = append(pm.patterns,
			routeMatch{pattern: "/" + m.ID},
			routeMatch{pattern: "/" + m.ID + "/", prefix: true})
	}
	for _, rt := range t.Routes() {
		pm.patterns = append(pm.patterns, routeMatch{pattern: rt.Pattern, prefix: rt.Prefix})
	}
	return pm, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 1) Registry-driven nav (the dashboard fetches this to render tool cards).
	if path == "/api/tools" && r.Method == http.MethodGet {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"tools": g.metas})
		return
	}

	// 2) Extracted tools -> reverse proxy to their service.
	for _, pm := range g.proxies {
		if pm.matches(path) {
			pm.proxy.ServeHTTP(w, r)
			return
		}
	}

	// 3) In-process API routes.
	for _, rt := range g.routes {
		if rt.method != r.Method {
			continue
		}
		hit := rt.pattern == path || (rt.prefix && strings.HasPrefix(path, rt.pattern))
		if hit {
			if err := rt.handle(w, r); err != nil {
				httpx.WriteError(w, err)
			}
			return
		}
	}

	// 4) In-process tool pages.
	if id, ok := g.pages[path]; ok && r.Method == http.MethodGet && g.pageFn != nil {
		if body, ok := g.pageFn(id); ok {
			h := w.Header()
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
	}

	// 5) Nothing matched: defer to the fallthrough handler (legacy router during
	// migration), or 404 once nothing remains behind the Gateway.
	if g.Next != nil {
		g.Next.ServeHTTP(w, r)
		return
	}
	httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "not found"))
}
