package server

import (
	"io"
	"net/http"
	"net/http/httptest"
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
	t.Setenv("DEVHUB_API_TOKEN", testToken)
	t.Setenv("DEVHUB_PORT", "")
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	settings, err := st.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	srv, err := New(st, devhub.Assets, settings, true, "test")
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
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
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

func TestToolAssetsServed(t *testing.T) {
	s := newTestServer(t)

	// The git page's split stylesheet: served with the CSS content type so the
	// browser applies it under strict MIME checking.
	rr := s.do("GET", "/tools/git/git.css", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /tools/git/git.css = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("git.css Content-Type = %q, want text/css; charset=utf-8", ct)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("git.css Cache-Control = %q, want no-store", got)
	}

	// A split feature script: served as JS.
	rr = s.do("GET", "/tools/git/core.js", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /tools/git/core.js = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
		t.Errorf("core.js Content-Type = %q, want application/javascript; charset=utf-8", ct)
	}

	// A tool's index.html must NOT be served raw here: those are token-injected
	// by the gateway at the exact /<tool> route. Serving the raw file would leak
	// an un-shimmed page whose /api/ calls carry no token.
	rr = s.do("GET", "/tools/git/index.html", goodHost, "", "", nil)
	if rr.Code == http.StatusOK {
		t.Errorf("GET /tools/git/index.html = 200, want non-200 (raw page must not be served)")
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

// The shared "contract" JS modules (dom.js/net.js) are served straight from the
// embed via the recursive shared/ walk, with a JS content-type. This guards the
// serving path that lets tool pages drop their copy-pasted escapeHtml/apiJson.
func TestSharedContractModulesServed(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct{ path, wantSubstr string }{
		{"/shared/dom.js", "function escapeHtml"},
		{"/shared/net.js", "async function apiJson"},
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
