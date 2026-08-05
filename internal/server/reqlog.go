package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/sanitize"
)

const (
	// maxLoggedErrBytes caps how much of a failing response is kept in the log
	// as readable text. A devhub error body is a small JSON envelope, so this is
	// generous for the case it exists to serve; successful responses are never
	// captured at all, because they include things like whole git diffs.
	maxLoggedErrBytes = 512

	// maxParsedErrBytes caps how much is buffered for errCode to parse. It is
	// larger than the display cap on purpose, and the two are separate on
	// purpose: the code is read back out of the JSON envelope, so a body cut
	// mid-object parses as nothing and the code silently becomes "".
	//
	// Tying that to the display cap made the failure mode absurd — writing one
	// longer hint would blank the code column for exactly the errors this log
	// exists to make searchable, and nothing would say why. The excerpt is still
	// truncated to maxLoggedErrBytes; only the parse sees the rest.
	maxParsedErrBytes = 8192
)

// loggablePath reports whether a request should be recorded.
//
// Only the two API surfaces are logged. Page and asset requests are left out
// deliberately: they are the bulk of the traffic, they say nothing about what
// was done to the machine, and at a fixed ring size every one of them would
// evict something that does.
//
// Reading the log is excluded — searching it would otherwise fill it with
// records of the searching, and each poll from an open /logs page would push
// out the entries the page is there to show.
//
// Changing it is not. Clearing and archiving are the two operations that alter
// the record itself, and leaving them out made the log unable to account for
// its own gaps: a wipe looked exactly like a quiet hour. The clear survives its
// own wipe because ServeHTTP adds the entry after the handler has run, so an
// emptied ring still holds the one line saying who emptied it.
func loggablePath(path string) bool {
	switch path {
	case "/api/logs/clear", "/api/logs/archive", "/ai-api/logs/clear", "/ai-api/logs/archive":
		return true
	}
	switch {
	case strings.HasPrefix(path, "/api/logs"), strings.HasPrefix(path, "/ai-api/logs"):
		return false
	case strings.HasPrefix(path, "/api/"), strings.HasPrefix(path, "/ai-api/"):
		return true
	}
	return false
}

// maxLoggedQueryRunes bounds the recorded query string. Mirrors the body
// summary's cap: a filter value or a long path can be arbitrarily long, and the
// ring's memory budget assumes entries stay small.
const maxLoggedQueryRunes = 512

// requestLabel returns the surface a request arrived on and the path to record
// for it.
//
// Two things happen here that the raw URL does not give us:
//
// The /ai-api prefix is normalized away, so both surfaces record the route they
// actually reached. The surface column already says which door was used, and
// without this a path filter typed as "/api/git" silently excludes every agent
// request — the exact traffic the log exists to show.
//
// The query string is kept, redacted. Leaving it out was the log's largest
// blind spot: the side-effecting GETs put their arguments there, so
// "GET /api/open?path=/etc" was recorded as "GET /api/open" and the one fact
// worth auditing — what was opened — was the one fact missing.
func requestLabel(r *http.Request) (surface, label string) {
	path := r.URL.Path
	surface = reqlog.SurfaceAPI
	if rest, ok := strings.CutPrefix(path, "/ai-api/"); ok {
		surface, path = reqlog.SurfaceAIAPI, "/api/"+rest
	}
	return surface, path + redactedQuery(r.URL)
}

// redactedQuery renders a URL's query with secret-looking values masked, or ""
// when there is none. The leading "?" is included so callers can concatenate.
//
// Values are masked by the same key heuristic the body summary uses, so a token
// passed as a query parameter is hidden the same way one passed in a body is.
// Re-encoding through url.Values sorts the keys, which also makes the string
// deterministic — it is used as an approval detail, and a rule must not depend
// on the order a client happened to send its parameters in.
//
// A malformed query yields whatever ParseQuery could recover; the unparseable
// remainder is dropped rather than echoed, because "could not parse it" is not
// a reason to copy unexamined bytes into a log that gets archived to disk.
func redactedQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	q, _ := url.ParseQuery(u.RawQuery)
	if len(q) == 0 {
		return "?(unparseable query)"
	}
	for k, vs := range q {
		if sanitize.IsSecretKey(k) {
			for i := range vs {
				vs[i] = "***"
			}
		}
	}
	return "?" + truncateRunes(q.Encode(), maxLoggedQueryRunes)
}

// statusRecorder wraps a ResponseWriter to observe what was sent back: the
// status, the byte count, and — for failures only — a capped copy of the body,
// which is where the error's stable `code` can be read back out.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	bytes   int
	errBody []byte
	// wroteHeader guards against a handler calling WriteHeader twice; the first
	// call is the one that reached the client.
	wroteHeader bool
}

func (rec *statusRecorder) WriteHeader(status int) {
	if rec.wroteHeader {
		return
	}
	rec.status = status
	rec.wroteHeader = true
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	// An implicit 200: a handler that writes without calling WriteHeader.
	rec.wroteHeader = true
	if rec.status >= 400 && len(rec.errBody) < maxParsedErrBytes {
		rec.errBody = append(rec.errBody, b[:min(len(b), maxParsedErrBytes-len(rec.errBody))]...)
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += n
	return n, err
}

// Flush forwards to the underlying writer.
//
// This is not optional. /api/restart, /api/rebuild and /api/update/apply write
// an acknowledgement, flush it, and then re-exec or replace the process a
// fraction of a second later. They reach for the flush through a
// w.(http.Flusher) type assertion — which a wrapper that does not implement
// Flush silently fails, leaving the ack buffered in a process that is about to
// die and the caller waiting for a reply that never comes.
func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the writer underneath, so http.ResponseController reaches any
// capability this wrapper does not forward by hand.
//
// Flush is forwarded explicitly above because three handlers assert
// w.(http.Flusher) directly and would silently stop flushing without it.
// Hijacker, ReaderFrom and Pusher have no caller today — but the next person to
// add streaming would find them missing, and the failure would again be silence
// rather than a compile error. One method closes that whole class.
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// errExcerpt returns the captured failure body as a single line, truncated to
// the display cap, or "" for a success.
func (rec *statusRecorder) errExcerpt() string {
	s := strings.Join(strings.Fields(string(rec.errBody)), " ")
	if len(s) > maxLoggedErrBytes {
		s = s[:maxLoggedErrBytes]
	}
	return s
}

// errCode pulls the stable error code out of a captured failure body. Reading it
// back off the wire keeps the code in the log identical to the one the caller
// saw, without threading it separately through every branch that can fail.
func (rec *statusRecorder) errCode() string {
	if len(rec.errBody) == 0 {
		return ""
	}
	var env struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.errBody, &env); err != nil {
		return "" // truncated or non-JSON body: no code to report
	}
	return env.Code
}
