package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// maxLoggedErrBytes caps how much of a failing response is kept in the log. A
// devhub error body is a small JSON envelope, so this is generous for the case
// it exists to serve; successful responses are never captured at all, because
// they include things like whole git diffs.
const maxLoggedErrBytes = 512

// loggablePath reports whether a request should be recorded.
//
// Only the two API surfaces are logged. Page and asset requests are left out
// deliberately: they are the bulk of the traffic, they say nothing about what
// was done to the machine, and at a fixed ring size every one of them would
// evict something that does.
//
// The log's own endpoints are excluded too — searching the log would otherwise
// fill it with records of the searching, and each poll from an open /logs page
// would push out the entries the page is there to show.
func loggablePath(path string) bool {
	switch {
	case strings.HasPrefix(path, "/api/logs"), strings.HasPrefix(path, "/ai-api/logs"):
		return false
	case strings.HasPrefix(path, "/api/"), strings.HasPrefix(path, "/ai-api/"):
		return true
	}
	return false
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
	if rec.status >= 400 && len(rec.errBody) < maxLoggedErrBytes {
		rec.errBody = append(rec.errBody, b[:min(len(b), maxLoggedErrBytes-len(rec.errBody))]...)
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

// errExcerpt returns the captured failure body as a single line, or "" for a
// success.
func (rec *statusRecorder) errExcerpt() string {
	return strings.Join(strings.Fields(string(rec.errBody)), " ")
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
