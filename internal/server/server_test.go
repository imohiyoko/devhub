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
	if rr := s.do("POST", "/api/bogus", goodHost, testToken, "", strings.NewReader("{}")); rr.Code != http.StatusNotFound {
		t.Errorf("unknown POST = %d, want 404", rr.Code)
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
