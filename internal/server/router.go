package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/approval"
	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/reqlog"
)

// approvalTimeout is how long an /ai-api write blocks waiting for the user to
// decide. It is named because the approval_timeout hint quotes it: the number a
// caller is told to expect and the number it actually waits must be the same.
// A var, not a const, only so tests can shorten it — nothing at runtime writes it.
var approvalTimeout = 60 * time.Second

// baseURL is devhub's own dashboard address, for hints that need to tell a
// caller where the user should look. It uses the bound port, not the configured
// one, so it stays right under a DEVHUB_PORT override.
func (s *Server) baseURL() string { return fmt.Sprintf("http://localhost:%d", s.port) }

// tokenlessAlternative explains, for a path that just failed the token check,
// what a non-browser caller should do instead.
//
// Usually that is the same path under /ai-api. The approval endpoints are the
// exception, and not an accidental one: they are reachable only with the token,
// so a caller on /ai-api cannot approve its own pending write. Pointing them at
// /ai-api/approval/… would send them to a 404 and imply a self-approval route
// exists, which is exactly what must not exist.
//
// The bare /api/approval is matched too. It is not a route, so it reaches the
// token check like any other unknown path — and a test that required the
// trailing slash would hand it the generic hint, naming /ai-api/approval: the
// one string this function exists to never produce.
func (s *Server) tokenlessAlternative(apiPath string) string {
	if apiPath == "/api/approval" || strings.HasPrefix(apiPath, "/api/approval/") {
		return fmt.Sprintf("The approval endpoints require the token that devhub injects into its own pages, and have no /ai-api equivalent — a caller cannot approve its own request. Ask the user to decide at %s.", s.baseURL())
	}
	return fmt.Sprintf("/api needs the per-session token that devhub injects into its own pages. A local agent or CLI should call %s instead — same route, no token, though writes wait for the user to approve them.",
		"/ai-api/"+strings.TrimPrefix(apiPath, "/api/"))
}

// markApproval records how an approval-gated request was decided. e is nil for
// a request that is not being logged, which is the normal case for the log's own
// endpoints — so this tolerates it rather than making every call site check.
func markApproval(e *reqlog.Entry, outcome string) {
	if e != nil {
		e.Approval = outcome
	}
}

// captureBody reads a request body, restores it for the downstream handler, and
// returns a redacted single-line summary. The summary also lands on the log
// entry, so the approval prompt and the log always show the same text and a body
// is read and redacted exactly once.
//
// Redaction is summarizeApprovalBody's, unchanged: secret-bearing keys become
// "***" before the value reaches either destination.
func (s *Server) captureBody(r *http.Request, e *reqlog.Entry) string {
	if r.Body == nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxApprovalBodyBytes))
	r.Body = io.NopCloser(bytes.NewReader(body))
	summary := summarizeApprovalBody(body)
	if e != nil {
		e.Body = summary
	}
	return summary
}

// ServeHTTP records the request, then serves it. Everything about how a request
// is handled lives in serve; this wrapper only observes, so a change to the
// security gate below cannot accidentally bypass the log.
//
// The entry is added after serve returns rather than deferred, because serve is
// synchronous and a panic should not leave a half-finished entry claiming a
// status the caller never received.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !loggablePath(r.URL.Path) {
		s.serve(w, r, nil)
		return
	}
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	e := reqlog.Begin(r)

	s.serve(rec, r, e)

	e.Finish(rec.status, rec.bytes, rec.errExcerpt(), time.Since(start))
	e.Code = rec.errCode()
	s.rlog.Add(e)
}

// serve is the single security gate (host allowlist + API token). Once a
// request clears the gate it is handed to the core gateway, which serves all
// tools (registry-driven) plus GET /api/tools, and falls through to serveSystem
// for the dashboard root and system endpoints.
//
// e is the log entry being built for this request, or nil when the request is
// not logged. Only the approval outcome is recorded here — it is knowable
// nowhere else — via markApproval, which tolerates a nil entry.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, e *reqlog.Entry) {
	if !s.hostAllowed(r) {
		httpx.WriteError(w, httpx.Errorf(http.StatusForbidden, "forbidden").WithHint(
			"host_not_allowed",
			fmt.Sprintf("Address devhub as %s. A Host header naming anything other than localhost/127.0.0.1 on that port is rejected.", s.baseURL())))
		return
	}

	// The listener binds to 127.0.0.1 only, so in normal operation every
	// request already arrives from loopback. Enforcing it per-request is
	// defense-in-depth for the token-bearing pages (the dashboard and tool
	// pages embed window.__DEVHUB_TOKEN__): if the process is ever exposed
	// through a forwarder or a future bind-address change, a remote client
	// must not receive a page with the API token baked in.
	if !s.isLoopback(r) {
		httpx.WriteError(w, httpx.Errorf(http.StatusForbidden, "forbidden").WithHint(
			"not_loopback",
			"devhub only answers connections from the same machine (127.0.0.1 / ::1). Run the client on this host; it cannot be reached over the network."))
		return
	}

	// Baseline security headers on every response. nosniff stops MIME
	// confusion on served assets; the two frame headers keep devhub pages out
	// of <iframe>s (clickjacking toward the approval panel). The frontend
	// never frames its own pages, so DENY / 'none' is safe.
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")

	// Handle AI API endpoints: bypass regular API token validation, but enforce loopback connection.
	if strings.HasPrefix(r.URL.Path, "/ai-api/") {
		if !s.isLoopback(r) {
			httpx.WriteError(w, httpx.Errorf(http.StatusForbidden, "forbidden: loopback connection required").WithHint(
				"not_loopback",
				"devhub only answers connections from the same machine (127.0.0.1 / ::1). Run the client on this host; it cannot be reached over the network."))
			return
		}
		// Loopback alone does not prove the caller isn't a browser: a cross-origin
		// web page the user is merely visiting can fetch 127.0.0.1, and its request
		// still originates from loopback. The token-less /ai-api surface is meant
		// for local, non-browser agents; a browser tags cross/same-site requests
		// with Sec-Fetch-Site, so reject those. Legit CLI/agent clients send no
		// Sec-Fetch-Site and are unaffected.
		if !sameOriginOrNonBrowser(r) {
			httpx.WriteError(w, httpx.Errorf(http.StatusForbidden, "forbidden: cross-site request").WithHint(
				"cross_site",
				"This request carried a cross-site Sec-Fetch-Site header, which is how a web page the user is merely visiting looks. Call /ai-api from a CLI or HTTP client that does not send Sec-Fetch-Site."))
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
			// just the endpoint. The same summary is what the log stores, so a
			// body is read, restored and redacted exactly once per request.
			if summary := s.captureBody(r, e); summary != "" {
				detail += " " + summary
			} else {
				// Always record a body component so the detail — and any always-allow
				// rule derived from it — is specific to WHAT is written. A bodyless
				// write must not collapse to a bare "METHOD /path" pattern that, under
				// prefix matching, would then auto-approve a later write of ANY body to
				// the same path (e.g. setting `editor` to a shell command).
				detail += " (no request body)"
			}

			if s.approvalMgr.ShouldAutoApprove(action, detail) {
				// The rule matched, so nothing is shown and nobody is asked. Before
				// this was logged it was devhub's one truly invisible operation:
				// after a single "always allow", every later call through the rule
				// happened with no record anywhere.
				markApproval(e, reqlog.ApprovalAuto)
			} else {
				req := s.approvalMgr.Register(action, detail)
				decision, err := s.approvalMgr.Wait(req, approvalTimeout)
				// Rejected and timed out demand opposite responses from a caller —
				// give up versus try again — so they must not collapse into one
				// answer. Wait returns an error only on the timeout path, which is
				// what separates them here.
				switch {
				case err != nil:
					// Wait already flipped the timed-out request to Rejected, so it
					// is gone from the pending list: there is nothing left on the
					// dashboard for the user to click. The way through is to send
					// the request again and have someone waiting to approve it —
					// telling the caller to "go approve the pending request" would
					// send it hunting for something that no longer exists.
					markApproval(e, reqlog.ApprovalTimeout)
					httpx.WriteError(w, httpx.Errorf(http.StatusRequestTimeout, "approval timed out").WithHint(
						"approval_timeout",
						fmt.Sprintf("Nobody answered within %s, and the prompt is now gone. Ask the user to open %s and watch for the approval prompt, then send this request again.", approvalTimeout, s.baseURL())))
					return
				case decision != approval.Approved:
					markApproval(e, reqlog.ApprovalRejected)
					httpx.WriteError(w, httpx.Errorf(http.StatusForbidden, "approval rejected").WithHint(
						"approval_rejected",
						"The user declined this request. Do not retry it — ask the user what they want done instead."))
					return
				default:
					markApproval(e, reqlog.ApprovalManual)
				}
			}
		}

		s.gateway.ServeHTTP(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !s.apiAuthorized(r) {
			// The token lives in the served pages, so a caller that isn't a browser
			// has no way to obtain it. Name the surface built for those callers
			// rather than leaving them to guess that /api is not the only door.
			httpx.WriteError(w, httpx.Errorf(http.StatusUnauthorized, "unauthorized").WithHint(
				"missing_token", s.tokenlessAlternative(r.URL.Path)))
			return
		}
		// Writes on this surface never reach the approval path, so this is the
		// only chance to capture their body for the log. Without it the dashboard's
		// own edits would appear in the log as a bare method and path.
		if e != nil && !isRead(r.Method) {
			s.captureBody(r, e)
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
	case r.Method == http.MethodGet && path == "/api/update/status":
		if err := s.handleUpdateStatus(w, r); err != nil {
			httpx.WriteError(w, err)
		}
		return
	case r.Method == http.MethodPost && path == "/api/update/apply":
		if err := s.handleUpdateApply(w, r); err != nil {
			httpx.WriteError(w, err)
		}
		return
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/shared/"):
		if body, ok := s.shared[path]; ok {
			writeAsset(w, body, assetContentType(path))
			return
		}
		httpx.WriteError(w, httpx.Errorf(http.StatusNotFound, "not found"))
		return
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/tools/"):
		// Per-tool static sub-assets (git.css and the split feature JS). Tool
		// index.html pages are excluded from s.toolAssets, so a request for one
		// 404s here instead of being served raw and un-shimmed — the shimmed page
		// lives at the gateway's exact /<tool> route.
		if body, ok := s.toolAssets[path]; ok {
			writeAsset(w, body, assetContentType(path))
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

// assetContentType picks the Content-Type for a static /shared/ or /tools/
// asset by extension. Only JS and CSS are served today; anything else keeps the
// historical JS default so existing assets are unaffected. A CSS file needs
// text/css or the browser refuses to apply it as a stylesheet under strict MIME
// checking.
func assetContentType(path string) string {
	if strings.HasSuffix(path, ".css") {
		return "text/css; charset=utf-8"
	}
	return "application/javascript; charset=utf-8"
}

// writeAsset writes an embedded static asset (e.g. shared JS/CSS) with the given
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
