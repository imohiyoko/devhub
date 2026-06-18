package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/imohiyoko/devhub/internal/httpx"
)

// routeFiles maps a request path to the embedded HTML file it serves
// (csv-tsv is intentionally absent; it has no page).
var routeFiles = map[string]string{
	"/":               "dashboard/index.html",
	"/diff-kun":       "tools/diff-kun/index.html",
	"/diff-kun/":      "tools/diff-kun/index.html",
	"/workspace":      "tools/workspace/index.html",
	"/workspace/":     "tools/workspace/index.html",
	"/diagram":        "tools/diagram/index.html",
	"/diagram/":       "tools/diagram/index.html",
	"/db-table":       "tools/db-table/index.html",
	"/db-table/":      "tools/db-table/index.html",
	"/ports":          "tools/ports/index.html",
	"/ports/":         "tools/ports/index.html",
	"/env-launcher":   "tools/env-launcher/index.html",
	"/env-launcher/":  "tools/env-launcher/index.html",
	"/git":            "tools/git/index.html",
	"/git/":           "tools/git/index.html",
}

// ServeHTTP is the single security gate (host allowlist + API token), then
// dispatches by method (GET handlers before POST).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r) {
		httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") && !s.apiAuthorized(r) {
		httpx.WriteJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.routeGET(w, r)
	case http.MethodPost:
		s.routePOST(w, r)
	default:
		httpx.WriteJSON(w, http.StatusNotImplemented, map[string]any{"error": "unsupported method"})
	}
}

func (s *Server) routeGET(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	var err error
	switch {
	case path == "/api/config" || path == "/api/settings" || strings.HasPrefix(path, "/api/settings/tool/"):
		err = s.settingsCtl.HandleGet(w, r)
	case path == "/api/repos":
		httpx.WriteJSON(w, http.StatusOK, s.gitCtl.AllRepos())
		return
	case path == "/api/envs" || strings.HasPrefix(path, "/api/envs/"):
		err = s.envsCtl.HandleGet(w, r)
	case strings.HasPrefix(path, "/api/git/"):
		err = s.gitCtl.HandleGet(w, r)
	case path == "/api/ls":
		err = s.workspaceCtl.HandleLs(w, r)
	case path == "/api/open":
		err = s.workspaceCtl.HandleOpen(w, r)
	case path == "/api/db/tables" || path == "/api/db/rows":
		err = s.databaseCtl.HandleGet(w, r)
	case path == "/api/ports":
		err = s.portsCtl.HandleGet(w, r)
	case path == "/api/info":
		s.handleInfo(w, r)
		return
	default:
		if body, ok := s.staticByRoute[path]; ok {
			h := w.Header()
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Content-Length", strconv.Itoa(len(body)))
			h.Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
		// Unknown API routes get a 404 (like POST); the redirect-to-/ fallback is
		// only meaningful for SPA navigation, not for /api clients.
		if strings.HasPrefix(path, "/api/") {
			httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "not found"))
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if err != nil {
		httpx.WriteError(w, err)
	}
}

func (s *Server) routePOST(w http.ResponseWriter, r *http.Request) {
	// Cap the request body so a malformed/oversized POST can't exhaust memory.
	// 10 MiB is generous for the local JSON payloads this server handles.
	const maxBodyBytes = 10 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, _ := io.ReadAll(r.Body)
	data := map[string]any{}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &data) // bad/empty body -> empty map
	}
	path := r.URL.Path
	var err error
	switch {
	case path == "/api/config" || path == "/api/settings" || strings.HasPrefix(path, "/api/settings/tool/"):
		err = s.settingsCtl.HandlePost(w, r, data)
	case strings.HasPrefix(path, "/api/git/"):
		err = s.gitCtl.HandlePost(w, r, data)
	case strings.HasPrefix(path, "/api/db/"):
		err = s.databaseCtl.HandlePost(w, r, data)
	case strings.HasPrefix(path, "/api/envs"):
		err = s.envsCtl.HandlePost(w, r, data)
	case strings.HasPrefix(path, "/api/ports/"):
		err = s.portsCtl.HandlePost(w, r, data)
	case path == "/api/restart":
		err = s.handleRestart(w, r)
	default:
		err = httpx.Errorf(http.StatusNotFound, "not found")
	}
	if err != nil {
		httpx.WriteError(w, err)
	}
}
