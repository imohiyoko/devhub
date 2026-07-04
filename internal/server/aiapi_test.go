package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
