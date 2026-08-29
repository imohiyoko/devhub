package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/storage"
)

const (
	testToken = "testtok"
	goodHost  = "localhost:8765" // matches default port from server.example.json
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return newTestServerOn(t, st)
}

// newTestServerOn builds a server over a store the caller owns, so a test can
// point two servers at the same one.
func newTestServerOn(t *testing.T, st *storage.Store) *Server {
	t.Helper()
	t.Setenv("DEVHUB_API_TOKEN", testToken)
	t.Setenv("DEVHUB_PORT", "")
	settings, err := st.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	srv, err := New(st, devhub.Assets, devhub.Docs, settings, true, "test")
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return srv
}

func (s *Server) do(method, target, host, token, sfs string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	req.Host = host
	// httptest defaults RemoteAddr to 192.0.2.1 (non-loopback), which the
	// security gate now rejects; simulate the normal local client.
	req.RemoteAddr = "127.0.0.1:54321"
	if token != "" {
		req.Header.Set("X-Devhub-Token", token)
	}
	if strings.HasPrefix(target, "/ai-api/") {
		req.Header.Set("X-Devhub-Agent-Token", s.agentToken)
	}
	if sfs != "" {
		req.Header.Set("Sec-Fetch-Site", sfs)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	return rr
}

func TestAIAPIRequiresSameUserToken(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/ai-api/envs", nil)
	req.Host = goodHost
	req.RemoteAddr = "127.0.0.1:54321"

	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing agent token = %d, want 401", rr.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/ai-api/envs", nil)
	req.Host = goodHost
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Devhub-Agent-Token", s.agentToken)
	rr = httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid agent token = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestInvalidEnvsBodyDoesNotReplaceStoredDocument(t *testing.T) {
	s := newTestServer(t)
	want := map[string]any{"version": float64(1), "environments": []any{
		map[string]any{"id": "keep", "name": "Keep", "repo": t.TempDir()},
	}}
	if err := s.store.SaveEnvs(want); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"empty", "", http.StatusBadRequest},
		{"malformed", `{"environments":[}`, http.StatusBadRequest},
		{"null", "null", http.StatusBadRequest},
		{"oversized padding", `{}` + strings.Repeat(" ", (10<<20)+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := s.do(http.MethodPost, "/api/envs", goodHost, testToken, "", strings.NewReader(tc.body))
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			got, err := s.store.LoadEnvs()
			if err != nil {
				t.Fatal(err)
			}
			bGot, _ := json.Marshal(got)
			bWant, _ := json.Marshal(want)
			if string(bGot) != string(bWant) {
				t.Fatalf("stored document changed:\n got %s\nwant %s", bGot, bWant)
			}
		})
	}
}

func TestStaticServesDashboardWithShim(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "X-Devhub-Token") {
		t.Error("dashboard missing token shim")
	}
	if !strings.Contains(body, `var T="`+testToken+`"`) {
		t.Error("dashboard missing injected token value")
	}
	for _, id := range []string{`id="serverPort"`, `id="dbLocalOnly"`, `id="serverSave"`} {
		if !strings.Contains(body, id) {
			t.Errorf("dashboard missing %s", id)
		}
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	// The terminal-settings UI (emulator/shell editor) ships in the dashboard.
	if !strings.Contains(body, `id="terminalSettings"`) {
		t.Error("dashboard missing terminal-settings UI")
	}
}

func TestHostAllowlist(t *testing.T) {
	s := newTestServer(t)
	if rr := s.do("GET", "/api/info", "evil.example.com", testToken, "", nil); rr.Code != http.StatusForbidden {
		t.Errorf("bad Host = %d, want 403", rr.Code)
	}
	if rr := s.do("GET", "/", "evil.example.com", "", "", nil); rr.Code != http.StatusForbidden {
		t.Errorf("bad Host (static) = %d, want 403", rr.Code)
	}
}

func TestNonLoopbackRejected(t *testing.T) {
	s := newTestServer(t)
	// A non-loopback client (httptest's default 192.0.2.1) must never receive
	// a page: the dashboard and tool pages carry the API token baked in.
	for _, target := range []string{"/", "/git", "/api/info", "/ai-api/git/status"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = goodHost
		req.Header.Set("X-Devhub-Token", testToken)
		rr := httptest.NewRecorder()
		s.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("non-loopback GET %s = %d, want 403", target, rr.Code)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rr.Code)
	}
	for k, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "frame-ancestors 'none'",
	} {
		if got := rr.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestAPIRequiresToken(t *testing.T) {
	s := newTestServer(t)
	if rr := s.do("GET", "/api/info", goodHost, "", "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rr.Code)
	}
	if rr := s.do("GET", "/api/info", goodHost, "wrong", "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", rr.Code)
	}
	rr := s.do("GET", "/api/info", goodHost, testToken, "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("good token = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"port":8765`) {
		t.Errorf("/api/info body = %s, want port 8765", rr.Body.String())
	}
	// The frontend's rebuild restart-detection depends on a non-empty per-process
	// `instance` id; a regression that dropped it would silently reintroduce the
	// "restart never finishes" hang.
	if !strings.Contains(rr.Body.String(), `"instance":"`) {
		t.Errorf("/api/info body = %s, want non-empty instance", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"pid":`) {
		t.Errorf("/api/info body = %s, want listener pid", rr.Body.String())
	}
	// The dashboard's terminal-settings UI reads `system` to pick which OS's
	// terminal config (emulator/shell) it edits.
	if !strings.Contains(rr.Body.String(), `"system":"`) {
		t.Errorf("/api/info body = %s, want system field", rr.Body.String())
	}
}

func TestSecFetchSite(t *testing.T) {
	s := newTestServer(t)
	if rr := s.do("GET", "/api/info", goodHost, testToken, "cross-site", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("cross-site = %d, want 401", rr.Code)
	}
	if rr := s.do("GET", "/api/info", goodHost, testToken, "same-origin", nil); rr.Code != http.StatusOK {
		t.Errorf("same-origin = %d, want 200", rr.Code)
	}
}

func TestUnknownRoutes(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/does-not-exist", goodHost, "", "", nil)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/" {
		t.Errorf("unknown GET = %d loc=%q, want 302 /", rr.Code, rr.Header().Get("Location"))
	}
	// Unknown API GETs should 404 rather than redirect (the SPA fallback is only
	// meaningful for navigations, not /api clients).
	if rr := s.do("GET", "/api/bogus", goodHost, testToken, "", nil); rr.Code != http.StatusNotFound {
		t.Errorf("unknown API GET = %d, want 404", rr.Code)
	}
	if rr := s.do("POST", "/api/bogus", goodHost, testToken, "", strings.NewReader("{}")); rr.Code != http.StatusNotFound {
		t.Errorf("unknown POST = %d, want 404", rr.Code)
	}
}

// TestToolPagesAndAssetsServed is registry-driven: for every tool the nav
// lists, the served page's /tools/ and /shared/ references must resolve with
// the extension-appropriate Content-Type (strict MIME checking refuses a
// stylesheet served as JS) and no-store caching, and the tool's raw
// index.html must not be reachable — pages are token-injected at /<id>, so
// serving the raw file would leak an un-shimmed page whose /api/ calls carry
// no token. Deriving the tool list from /api/tools and the asset list from
// each served page keeps split frontends (git: 11 scripts, env-launcher: 11)
// covered without ever editing this test.
func TestToolPagesAndAssetsServed(t *testing.T) {
	s := newTestServer(t)

	rr := s.do("GET", "/api/tools", goodHost, testToken, "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/tools = %d, want 200", rr.Code)
	}
	var nav struct {
		Tools []struct {
			ID string `json:"id"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &nav); err != nil {
		t.Fatalf("decode /api/tools: %v", err)
	}
	if len(nav.Tools) == 0 {
		t.Fatal("nav lists no tools")
	}

	refRe := regexp.MustCompile(`(?:src|href)="(/(?:tools|shared)/[^"]+)"`)
	for _, tool := range nav.Tools {
		rr := s.do("GET", "/"+tool.ID, goodHost, "", "", nil)
		if rr.Code != http.StatusOK {
			t.Errorf("GET /%s = %d, want 200", tool.ID, rr.Code)
			continue
		}
		refs := refRe.FindAllStringSubmatch(rr.Body.String(), -1)
		if len(refs) == 0 {
			t.Errorf("/%s references no static assets (split or template regression?)", tool.ID)
		}
		for _, m := range refs {
			path := m[1]
			ar := s.do("GET", path, goodHost, "", "", nil)
			if ar.Code != http.StatusOK {
				t.Errorf("GET %s = %d, want 200 (referenced by /%s)", path, ar.Code, tool.ID)
				continue
			}
			want := "application/javascript; charset=utf-8"
			if strings.HasSuffix(path, ".css") {
				want = "text/css; charset=utf-8"
			}
			if ct := ar.Header().Get("Content-Type"); ct != want {
				t.Errorf("%s Content-Type = %q, want %q", path, ct, want)
			}
			if cc := ar.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("%s Cache-Control = %q, want no-store", path, cc)
			}
		}
		if rr := s.do("GET", "/tools/"+tool.ID+"/index.html", goodHost, "", "", nil); rr.Code == http.StatusOK {
			t.Errorf("GET /tools/%s/index.html = 200, want non-200 (raw page must not be served)", tool.ID)
		}
	}
}

func TestSettingsRoundTripAndSanitize(t *testing.T) {
	s := newTestServer(t)
	body := `{"disabled_tools":["ports"],"db_connections":[{"name":"local","password":"s3cret"}]}`
	if rr := s.do("POST", "/api/settings", goodHost, testToken, "", strings.NewReader(body)); rr.Code != http.StatusOK {
		t.Fatalf("POST /api/settings = %d, want 200", rr.Code)
	}
	rr := s.do("GET", "/api/settings", goodHost, testToken, "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200", rr.Code)
	}
	got := rr.Body.String()
	if !strings.Contains(got, `"disabled_tools":["ports"]`) {
		t.Errorf("settings did not persist disabled_tools: %s", got)
	}
	if strings.Contains(got, "s3cret") || strings.Contains(got, "password") {
		t.Errorf("secret leaked in settings response: %s", got)
	}
}

func TestInvalidToolID(t *testing.T) {
	s := newTestServer(t)
	if rr := s.do("GET", "/api/settings/tool/Bad_ID!", goodHost, testToken, "", nil); rr.Code != http.StatusBadRequest {
		t.Errorf("invalid tool_id = %d, want 400", rr.Code)
	}
}

// The shared "contract" JS modules (dom.js/net.js/modal.js) are served straight
// from the embed via the recursive shared/ walk, with a JS content-type. This
// guards the serving path that lets tool pages drop their copy-pasted
// escapeHtml/apiJson and their per-page modal keyboard handling: a page whose
// modal helper 404s renders a dialog that Tab walks straight out of, and
// nothing else on the page would fail.
func TestSharedContractModulesServed(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct{ path, wantSubstr string }{
		{"/shared/dom.js", "function escapeHtml"},
		{"/shared/net.js", "async function apiJson"},
		{"/shared/modal.js", "window.DevhubModal"},
	} {
		rr := s.do("GET", tc.path, goodHost, "", "", nil)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q, want application/javascript", tc.path, ct)
		}
		if !strings.Contains(rr.Body.String(), tc.wantSubstr) {
			t.Errorf("GET %s body missing %q", tc.path, tc.wantSubstr)
		}
	}
}

// A tool page that dropped its local copies must reference the shared modules,
// so the page still has escapeHtml/apiJson at runtime. db-table uses both.
func TestToolPageReferencesSharedModules(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/db-table", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /db-table = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`src="/shared/dom.js"`, `src="/shared/net.js"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/db-table page missing %s", want)
		}
	}
}

// The opt-in UI layer lives under shared/ui/ and rides the same recursive shared/
// walk, served with a CSS content-type so a strict-MIME browser applies it. This
// guards the path that lets adopter pages drop their duplicated app-shell CSS.
func TestSharedUIShellServed(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/shared/ui/shell.css", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /shared/ui/shell.css = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("shell.css Content-Type = %q, want text/css; charset=utf-8", ct)
	}
	if !strings.Contains(rr.Body.String(), ".hub-link") {
		t.Error("shell.css body missing .hub-link rule")
	}
}

// A page that dropped its local body/header block must link the shared shell, or
// its chrome would render unstyled. ports is one of the five adopter pages.
func TestToolPageReferencesShell(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/ports", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /ports = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `href="/shared/ui/shell.css"`) {
		t.Error("/ports page missing shell.css link")
	}
}

// components.css (the button/empty component layer) rides the same shared/ walk
// and is linked by the tool pages that dropped their local button CSS.
func TestSharedUIComponentsServed(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/shared/ui/components.css", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /shared/ui/components.css = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("components.css Content-Type = %q, want text/css; charset=utf-8", ct)
	}
	body := rr.Body.String()
	for _, want := range []string{".btn ", ".btn-primary", ".empty "} {
		if !strings.Contains(body, want) {
			t.Errorf("components.css missing %q rule", want)
		}
	}
}

// env-launcher opts out of shell.css but still links the component layer for its
// buttons — guard that a components-only adopter references it.
func TestToolPageReferencesComponents(t *testing.T) {
	s := newTestServer(t)
	rr := s.do("GET", "/env-launcher", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /env-launcher = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `href="/shared/ui/components.css"`) {
		t.Error("/env-launcher page missing components.css link")
	}
}
