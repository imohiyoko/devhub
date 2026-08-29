package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
//
// For whoever blocks a system route next: /api/restart and its neighbours are
// handled by serveSystem, not by a tool, so reg.Tools() does not know them and
// this test would report such a key as blocking nothing. Widen the route list
// below rather than dropping the assertion. ADR 0005 decision #8 records why
// restart is deliberately left on the approval gate today.
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
	reg := tools.Registry(st, docSet, reqlog.New(1, "test"), 8765)

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

// The blocklist is an exact-match map over a path nobody canonicalized:
// net/http.Server, unlike http.ServeMux, hands the handler the path as written.
// So "/ai-api/./logs/clear" does not match the key that withholds it, and the
// unconditional barrier is not the one holding these back.
//
// What holds them is the approval gate and, behind it, the gateway matching
// routes exactly or by prefix. Both are barriers this change argued are the
// wrong ones to rely on: decision #8 exists precisely because approval is one
// "always allow" away from automatic, and labelEscape escapes "?" rather than
// trusting the gateway's route matching.
//
// So the test stands where decision #8 says an agent can stand — past the
// approval gate — and pins the outcome rather than the mechanism. Whoever later
// adds path cleaning, or turns /api/logs/ into a prefix route, has to keep the
// handler behind a blocked path from running. The setup entry is the witness:
// clear empties the ring, so it surviving is proof the handler did not.
func TestNonCanonicalPathsDoNotReachABlockedHandler(t *testing.T) {
	// A safety net only. If the always-allow rule below stops matching, these
	// fall back to waiting for a user who is not here, and the test would take a
	// minute per spelling on its way to proving nothing.
	orig := approvalTimeout
	approvalTimeout = 10 * time.Millisecond
	t.Cleanup(func() { approvalTimeout = orig })

	for _, path := range []string{
		"/ai-api/./logs/clear",
		"/ai-api//logs/clear",
		"/ai-api/logs/./clear",
		"/ai-api/logs/../logs/clear",
		"/ai-api/logs/clear/",
		"/ai-api/logs/clear/.",
	} {
		srv := newTestServer(t)
		srv.do(http.MethodGet, "/api/ports", goodHost, testToken, "", nil) // the witness
		// The detail is "POST <path> (no request body)", so this prefix matches
		// it at a space boundary — the same way a rule the user actually clicked
		// would.
		srv.approvalMgr.AddAlwaysAllowRule("api_write", "POST "+path)

		rr := srv.do(http.MethodPost, path, goodHost, "", "", nil)
		// Without this the test can go vacuous without saying so: a rule that
		// stopped matching would leave the approval gate doing the stopping, and
		// every assertion below would pass no matter what the blocklist did.
		if rr.Code == http.StatusRequestTimeout {
			t.Fatalf("POST %s waited for approval — the rule no longer matches, so this test checks nothing", path)
		}
		if rr.Code == http.StatusOK {
			t.Errorf("POST %s = 200 — it reached a handler", path)
		}
		if n := len(srv.rlog.Query(reqlog.Filter{PathPrefix: "/api/ports"})); n != 1 {
			t.Errorf("POST %s emptied the ring — the clear handler ran behind the blocklist", path)
		}
	}
}
