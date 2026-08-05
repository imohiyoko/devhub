package server

import "net/http"

// sideEffectingGetPaths lists the (rewritten /api/…) GET endpoints that change
// host state rather than just reading it, so they must go through /ai-api's
// manual-approval gate even though they are GETs. Keep this in sync with any GET
// route whose handler launches a process or otherwise acts on the machine.
var sideEffectingGetPaths = map[string]bool{
	"/api/open": true, // workspace: opens a directory in the configured editor
}

// aiAPIBlockedPaths are rewritten /api paths that /ai-api must not reach, with
// the reason each is withheld. Keys are post-rewrite paths, matching what the
// router holds when it consults this.
//
// The approval endpoints are already unreachable by construction — they are
// served inside the token-checked branch, so an /ai-api caller falls through to
// a 404. The log's mutating endpoints are not: they are ordinary tool routes,
// and without this an agent could erase the record of what it had done.
//
// Approval alone does not cover it. Approval is one "always allow" away from
// being automatic, and that rule would be the last thing anyone could prove had
// ever been created — the wipe that followed would look like a quiet hour.
//
// Reading the log stays open on /ai-api. An agent diagnosing its own failures
// is why that surface exists; erasing the evidence is not.
var aiAPIBlockedPaths = map[string]string{
	"/api/logs/clear":   "Clearing the request log is deliberately unavailable to /ai-api — an agent must not be able to erase the record of what it did. Ask the user to clear it from the dashboard if that is really what they want.",
	"/api/logs/archive": "Archiving the request log is deliberately unavailable to /ai-api — what is kept past a restart is the user's decision. Read it with GET /ai-api/logs instead.",
}

// isRead reports whether a method only reads. It is the method half of the
// approval rule, reused where the question is "could this carry a body worth
// recording" rather than "does this need approval".
func isRead(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
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
