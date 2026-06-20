package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/imohiyoko/devhub/internal/httpx"
)

// ServeHTTP is the single security gate (host allowlist + API token). Once a
// request clears the gate it is handed to the core gateway, which serves all
// tools (registry-driven) plus GET /api/tools, and falls through to serveSystem
// for the dashboard root and system endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r) {
		httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && !s.apiAuthorized(r) {
		httpx.WriteJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	s.gateway.ServeHTTP(w, r)
}

// serveSystem handles what is not a tool: the dashboard root page, the
// process-level endpoints (/api/info, /api/restart), and the SPA redirect
// fallback. It is wired as the gateway's Next handler.
func (s *Server) serveSystem(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/info":
		s.handleInfo(w, r)
		return
	case r.Method == http.MethodPost && path == "/api/restart":
		if err := s.handleRestart(w, r); err != nil {
			httpx.WriteError(w, err)
		}
		return
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/shared/"):
		if body, ok := s.shared[path]; ok {
			writeAsset(w, body, "application/javascript; charset=utf-8")
			return
		}
		httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "not found"))
		return
	case r.Method == http.MethodGet && path == "/":
		writePage(w, s.dashboard)
		return
	}

	// Unknown API routes get a 404; an unknown page navigation redirects to the
	// dashboard (SPA-style), matching the prior behavior.
	if strings.HasPrefix(path, "/api/") {
		httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "not found"))
		return
	}
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "not found"))
}

// writePage writes a pre-rendered HTML page with no-store caching.
func writePage(w http.ResponseWriter, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// writeAsset writes an embedded static asset (e.g. shared JS) with the given
// content type. no-store keeps a restarted server from serving stale bytes.
func writeAsset(w http.ResponseWriter, body []byte, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
