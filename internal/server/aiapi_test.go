package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/core"
	docspkg "github.com/imohiyoko/devhub/internal/docs"
	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/storage"
	"github.com/imohiyoko/devhub/internal/tools"
)

func TestAiAPINeedsApproval(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/settings", true},
		{http.MethodPut, "/api/db/update", true},
		{http.MethodDelete, "/api/approval/rules/x", true},
		{http.MethodPatch, "/api/whatever", true},
		{http.MethodGet, "/api/git/status", false},
		{http.MethodGet, "/api/ls", false},
		{http.MethodGet, "/api/open", true}, // side-effecting: launches the editor
		{http.MethodHead, "/api/open", false},
		{http.MethodOptions, "/api/open", false},
	}
	for _, c := range cases {
		if got := aiAPINeedsApproval(c.method, c.path); got != c.want {
			t.Errorf("aiAPINeedsApproval(%q, %q) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestSameOriginOrNonBrowser(t *testing.T) {
	cases := []struct {
		sfs  string
		set  bool
		want bool
	}{
		{"", false, true},           // non-browser client (curl / local agent): no header
		{"none", true, true},        // user-initiated navigation
		{"same-origin", true, true}, // devhub's own page
		{"same-site", true, false},  // another localhost port/subdomain in the browser
		{"cross-site", true, false}, // a malicious web page the user is visiting
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ai-api/open", nil)
		if c.set {
			r.Header.Set("Sec-Fetch-Site", c.sfs)
		}
		if got := sameOriginOrNonBrowser(r); got != c.want {
			t.Errorf("sameOriginOrNonBrowser(Sec-Fetch-Site=%q) = %v, want %v", c.sfs, got, c.want)
		}
	}
}

// aiAPIBlockedPaths names routes by string, and nothing but this test ties
// those strings to the routes that actually exist. That coupling is silent in
// both directions:
//
// A rename or a move leaves a key blocking nothing. Every test still passes —
// including the ones asserting 403, because /ai-api/logs/clear would then reach
// no handler at all and 404. The blocklist would look like it was working.
//
// Turning a route into a Prefix one is worse. /api/logs/ with Prefix:true still
// serves /api/logs/clear, and the exact-match blocklist still answers 403 for
// that spelling — but /ai-api/logs/clear/ would now reach the handler, because
// a prefix route matches it and the blocklist does not. The refusal this change
// argues is the only real barrier would be one trailing slash away from gone,
// with a green suite.
//
// So: every key must name a route that exists, and no route may reach a key by
// prefix.
func TestBlockedPathsNameRoutesThatExistAndAreExact(t *testing.T) {
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	docSet, err := docspkg.Load(devhub.Docs)
	if err != nil {
		t.Fatalf("docs.Load: %v", err)
	}
	reg := tools.Registry(st, docSet, reqlog.New(1, "test"))

	var routes []core.Route
	for _, tool := range reg.Tools() {
		routes = append(routes, tool.Routes()...)
	}
	for blocked := range aiAPIBlockedPaths {
		var exact bool
		for _, rt := range routes {
			if rt.Pattern == blocked && !rt.Prefix {
				exact = true
			}
			if rt.Prefix && strings.HasPrefix(blocked, rt.Pattern) {
				t.Errorf("%q is blocked by exact match but served by the prefix route %q: /ai-api%s/ would reach the handler",
					blocked, rt.Pattern, strings.TrimPrefix(blocked, "/api"))
			}
		}
		if !exact {
			t.Errorf("%q blocks nothing: no tool declares it as an exact route (renamed or moved?)", blocked)
		}
	}
}
