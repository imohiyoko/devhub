// Package core defines the plugin contract every devhub tool implements, plus a
// gateway that mounts each tool either in-process (the single-binary default)
// or as a reverse proxy to an out-of-process tool service.
//
// The design goal is that "add a tool" is a closed operation: implement Tool,
// add it to the registry, and the gateway routing plus the dashboard's
// /api/tools nav are all derived from that registration. No core file changes
// per tool.
//
// It is also microservices-conscious without giving up the single binary:
// each tool is a bounded context that depends only on interfaces (Deps), owns
// its own storage namespace, and exposes a transport-agnostic HTTP contract.
// Because of that, a tool can later be "extracted" to its own process by
// pointing an Upstreams entry at it — a config change, not a rewrite. See
// README.md in this package.
package core

import "net/http"

// Handler is one tool endpoint. Returning an error lets the gateway centralize
// error->HTTP translation (httpx.WriteError). This mirrors the existing
// controllers' HandleGet/HandlePost(w, r) error shape, so wrapping them is
// mechanical.
type Handler func(w http.ResponseWriter, r *http.Request) error

// Meta is a tool's identity and dashboard presentation. ID doubles as the route
// namespace: the page is served at /<ID> and the API lives under /api/<ID>/.
type Meta struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Desc  string `json:"desc,omitempty"`
	Icon  string `json:"icon,omitempty"`
	// Page is the embedded HTML path (e.g. "tools/git/index.html"). Empty means
	// an API-only tool with no page (like the old csv-tsv route).
	Page string `json:"-"`
}

// Route is one API endpoint owned by a tool.
type Route struct {
	Method  string  // http.MethodGet / http.MethodPost
	Pattern string  // exact path ("/api/git/status") or prefix ("/api/git/")
	Prefix  bool    // match Pattern as a prefix when true
	Handle  Handler // the in-process implementation
}

// Tool is one self-contained devhub feature (git, ports, env-launcher, ...).
// Implementations are constructed from Deps and registered in the composition
// root.
type Tool interface {
	Meta() Meta
	Routes() []Route
}

// Deps is the typed dependency set handed to a tool's constructor. Cross-tool
// needs are expressed here as interfaces (not concrete imports), so tools stay
// decoupled: today's in-process call can become a remote call later without
// touching the caller. Extend it as tools require shared services, e.g.
//
//	type Deps struct {
//	    Store Store
//	    Repos RepoLister // provided by the git tool
//	    Ports PortService
//	}
type Deps struct {
	// Store is this tool's persistence seam. Hand each tool a namespaced view
	// (see Namespace) so two tools can never collide on keys.
	Store Store
}
