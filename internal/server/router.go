package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/approval"
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

	// Handle AI API endpoints: bypass regular API token validation, but enforce loopback connection.
	if strings.HasPrefix(r.URL.Path, "/ai-api/") {
		if !s.isLoopback(r) {
			httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden: loopback connection required"})
			return
		}

		origPath := r.URL.Path
		// Rewrite path: /ai-api/foo -> /api/foo to let the gateway handle it.
		r.URL.Path = "/api/" + strings.TrimPrefix(r.URL.Path, "/ai-api/")

		// For write operations (POST, PUT, DELETE, PATCH), wait for manual approval.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			action := "api_write"
			detail := r.Method + " " + origPath

			if !s.approvalMgr.ShouldAutoApprove(action, detail) {
				req := s.approvalMgr.Register(action, detail)
				// Wait up to 60 seconds for approval.
				decision, err := s.approvalMgr.Wait(req, 60*time.Second)
				if err != nil || decision != approval.Approved {
					httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "request rejected or timed out"})
					return
				}
			}
		}

		s.gateway.ServeHTTP(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !s.apiAuthorized(r) {
			httpx.WriteJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/approval/") {
			s.handleApproval(w, r)
			return
		}
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
	case r.Method == http.MethodPost && path == "/api/rebuild":
		if err := s.handleRebuild(w, r); err != nil {
			httpx.WriteError(w, err)
		}
		return
	case r.Method == http.MethodGet && path == "/api/rebuild/status":
		if err := s.handleRebuildStatus(w, r); err != nil {
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

func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if r.Method == http.MethodGet && path == "/api/approval/pending" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"requests": s.approvalMgr.ListPending()})
		return
	}
	if r.Method == http.MethodPost && path == "/api/approval/respond" {
		var body struct {
			ID       string            `json:"id"`
			Decision approval.Decision `json:"decision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, httpx.Errorf(http.StatusBadRequest, "invalid body: %v", err))
			return
		}
		if err := s.approvalMgr.Respond(body.ID, body.Decision); err != nil {
			httpx.WriteError(w, httpx.Errorf(http.StatusBadRequest, "respond failed: %v", err))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	if r.Method == http.MethodPost && path == "/api/approval/always-allow" {
		var body struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, httpx.Errorf(http.StatusBadRequest, "invalid body: %v", err))
			return
		}
		var foundReq *approval.Request
		for _, req := range s.approvalMgr.ListPending() {
			if req.ID == body.ID {
				foundReq = req
				break
			}
		}
		if foundReq == nil {
			httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "pending request not found"))
			return
		}

		s.approvalMgr.AddAlwaysAllowRule(foundReq.Action, foundReq.Detail)

		if err := s.approvalMgr.Respond(body.ID, approval.Approved); err != nil {
			httpx.WriteError(w, httpx.Errorf(http.StatusBadRequest, "respond failed: %v", err))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	if r.Method == http.MethodGet && path == "/api/approval/rules" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"rules": s.approvalMgr.ListRules()})
		return
	}
	if r.Method == http.MethodDelete && strings.HasPrefix(path, "/api/approval/rules/") {
		id := strings.TrimPrefix(path, "/api/approval/rules/")
		if err := s.approvalMgr.DeleteRule(id); err != nil {
			httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "rule not found: %v", err))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "not found"))
}
