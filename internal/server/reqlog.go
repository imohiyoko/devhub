package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/sanitize"
)

const (
	// maxLoggedErrRunes caps how much of a failing response is kept in the log
	// as readable text. A devhub error body is a small JSON envelope, so this is
	// generous for the case it exists to serve; successful responses are never
	// captured at all, because they include things like whole git diffs.
	maxLoggedErrRunes = 512

	// maxParsedErrBytes caps how much is buffered for errCode to parse. It is
	// larger than the display cap on purpose, and the two are separate on
	// purpose: the code is read back out of the JSON envelope, so a body cut
	// mid-object parses as nothing and the code silently becomes "".
	//
	// Tying that to the display cap made the failure mode absurd — writing one
	// longer hint would blank the code column for exactly the errors this log
	// exists to make searchable, and nothing would say why. The excerpt is still
	// truncated to maxLoggedErrRunes; only the parse sees the rest.
	maxParsedErrBytes = 8192
)

// loggablePath reports whether a request should be recorded. It takes the
// normalized path from requestLabel, so an /ai-api route arrives here already
// rewritten to /api — every rule below is written once instead of twice, and a
// rule added later cannot cover one surface while missing the other.
//
// Only the API is logged. Page and asset requests are left out deliberately:
// they are the bulk of the traffic, they say nothing about what was done to the
// machine, and at a fixed ring size every one of them would evict something
// that does.
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
	case "/api/logs/clear", "/api/logs/archive":
		return true
	}
	switch {
	case strings.HasPrefix(path, "/api/logs"):
		return false
	case strings.HasPrefix(path, "/api/"):
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
// The query is returned separately from the path so loggablePath can match on
// the route alone; the recorded label is the two concatenated.
//
// The path goes through the same escaping as the query. r.URL.Path is already
// percent-decoded, so a request for "/ai-api/x%20y" arrives with a space in it —
// the one character the query escaping exists to keep out of a detail. Nothing
// reaches a handler through such a path today (the gateway matches routes
// exactly or by prefix, and neither matches), but that is a property of the
// gateway, not of this string, and the two would have to stay in step forever
// for it to hold. Escaping both sides costs nothing and needs no such promise.
func requestLabel(r *http.Request) (surface, path, query string) {
	path, surface = r.URL.Path, reqlog.SurfaceAPI
	if rest, ok := strings.CutPrefix(path, "/ai-api/"); ok {
		surface, path = reqlog.SurfaceAIAPI, "/api/"+rest
	}
	return surface, labelEscape(path), redactedQuery(r.URL)
}

// labelEscape percent-escapes only what would otherwise let two different
// requests render as the same label: the characters this format uses as
// structure, the escape character itself, and the ASCII control range.
//
// url.Values.Encode is the obvious choice and the wrong one here. This string's
// primary reader is a human deciding whether to approve a request, and Encode
// turns "path=/Users/me/work" into "path=%2FUsers%2Fme%2Fwork" and a Japanese
// path into nine bytes per character — unreadable in the prompt, and unmatchable
// by the log's free-text filter, which searches the stored text literally. What
// the encoding has to guarantee is injectivity, not URL-safety: nothing parses
// this back.
//
// Space is escaped for a reason beyond legibility. The label feeds an approval
// detail, and detailMatchesPattern anchors a rule at a space — a value holding
// one could otherwise manufacture the boundary that lets a rule cover a request
// the user never saw. "?" is escaped for the same kind of reason: the path and
// the query are joined with it, so a path containing one (r.URL.Path is already
// percent-decoded, so "%3F" arrives here as "?") could imitate a query that was
// never sent.
//
// The loop is over bytes, not runes, and that is load-bearing. Ranging over a
// string yields U+FFFD for every byte of invalid UTF-8, so "?a=%FF" and
// "?a=%FE" would collapse to the same label — and an always-allow rule for one
// would cover the other. Valid UTF-8 passes through byte by byte unchanged, so
// nothing legible is lost. The trade is that non-ASCII control characters
// (U+0085 and the rest of C1) are no longer escaped; none of them is a
// separator here, so none can forge a boundary.
func labelEscape(s string) string {
	var b strings.Builder
	for i := range len(s) {
		if c := s[i]; c == '%' || c == '&' || c == '=' || c == '?' || c == ' ' || c < 0x20 || c == 0x7F {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// redactedQuery renders a URL's query with secret-looking values masked, or ""
// when there is none. The leading "?" is included so callers can concatenate.
//
// Values are masked by the same key heuristic the body summary uses, so a token
// passed as a query parameter is hidden the same way one passed in a body is.
// Keys are sorted, which makes the string deterministic — it is used as an
// approval detail, and a rule must not depend on the order a client happened to
// send its parameters in.
//
// A malformed query yields whatever ParseQuery could recover; the unparseable
// remainder is dropped rather than echoed, because "could not parse it" is not
// a reason to copy unexamined bytes into a log that gets archived to disk.
//
// Known limit: two queries longer than the cap can truncate to the same string,
// and an always-allow rule on one would then cover the other. No devhub route
// takes a query that long, so this is recorded rather than solved.
func redactedQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	q, _ := url.ParseQuery(u.RawQuery)
	if len(q) == 0 {
		return "?(unparseable query)"
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(q)) {
		for _, v := range q[k] {
			if sanitize.IsSecretKey(k) {
				v = "***"
			}
			parts = append(parts, labelEscape(k)+"="+labelEscape(v))
		}
	}
	return "?" + truncateRunes(strings.Join(parts, "&"), maxLoggedQueryRunes)
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
//
// The cut is by rune, not by byte. Error bodies carry messages from git, the
// database drivers and the shell, so multibyte text is ordinary here — a byte
// cut would leave a partial sequence that becomes U+FFFD on the way to SQLite.
// truncateRunes also marks the cut, so a reader can tell the message ended from
// one that was trimmed.
func (rec *statusRecorder) errExcerpt() string {
	return truncateRunes(strings.Join(strings.Fields(string(rec.errBody)), " "), maxLoggedErrRunes)
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
