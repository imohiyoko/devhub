package server

import (
	"bytes"
	"encoding/json"
	"io"
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
		// Loopback alone does not prove the caller isn't a browser: a cross-origin
		// web page the user is merely visiting can fetch 127.0.0.1, and its request
		// still originates from loopback. The token-less /ai-api surface is meant
		// for local, non-browser agents; a browser tags cross/same-site requests
		// with Sec-Fetch-Site, so reject those. Legit CLI/agent clients send no
		// Sec-Fetch-Site and are unaffected.
		if !sameOriginOrNonBrowser(r) {
			httpx.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden: cross-site request"})
			return
		}

		origPath := r.URL.Path
		// Rewrite path: /ai-api/foo -> /api/foo to let the gateway handle it.
		r.URL.Path = "/api/" + strings.TrimPrefix(r.URL.Path, "/ai-api/")

		// Writes — and the few GET endpoints that have side effects (e.g.
		// /api/open launches the editor) rather than just reading state — wait for
		// manual approval. Plain reads pass straight through.
		if aiAPINeedsApproval(r.Method, r.URL.Path) {
			action := "api_write"
			detail := r.Method + " " + origPath

			// Include a redacted preview of the request body so the approval
			// prompt — and any always-allow rule derived from it — reflects WHAT
			// is being written (e.g. setting `editor` to a shell command), not
			// just the endpoint. Read then restore the body so the downstream
			// handler still sees it; secret-bearing fields are masked so they
			// never reach the prompt or a persisted rule.
			if r.Body != nil {
				body, _ := io.ReadAll(io.LimitReader(r.Body, maxApprovalBodyBytes))
				r.Body = io.NopCloser(bytes.NewReader(body))
				// Always record a body component so the detail — and any always-allow
				// rule derived from it — is specific to WHAT is written. A bodyless
				// write must not collapse to a bare "METHOD /path" pattern that, under
				// prefix matching, would then auto-approve a later write of ANY body to
				// the same path (e.g. setting `editor` to a shell command).
				if summary := summarizeApprovalBody(body); summary != "" {
					detail += " " + summary
				} else {
					detail += " (no request body)"
				}
			}

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
