package server

import "net/http"

// sideEffectingGetPaths lists the (rewritten /api/…) GET endpoints that change
// host state rather than just reading it, so they must go through /ai-api's
// manual-approval gate even though they are GETs. Keep this in sync with any GET
// route whose handler launches a process or otherwise acts on the machine.
var sideEffectingGetPaths = map[string]bool{
	"/api/open": true, // workspace: opens a directory in the configured editor
}

// aiAPINeedsApproval reports whether an /ai-api request must wait for manual
// approval: every state-changing method, plus the handful of GET endpoints with
// side effects. HEAD/OPTIONS and plain reads never require approval.
//
// apiPath is the request path after /ai-api has been rewritten to /api.
func aiAPINeedsApproval(method, apiPath string) bool {
	switch method {
	case http.MethodGet:
		return sideEffectingGetPaths[apiPath]
	case http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}
