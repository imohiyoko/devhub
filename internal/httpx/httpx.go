// Package httpx provides the shared JSON response writer and HTTP error type
// used by the server and all controllers. WriteJSON emits UTF-8 JSON without
// HTML escaping of <, >, & so values such as git diff output round-trip
// byte-for-byte.
package httpx

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"

	"github.com/imohiyoko/devhub/internal/jsonx"
)

// HTTPError carries an explicit status code (and optional extra fields) for an
// API error. Controllers return it to signal a specific status; any other error
// is treated as a 400 by WriteError.
//
// Code and Hint exist for non-human callers. Msg is prose and may be reworded at
// any time, so a client that branches on it breaks silently; Code is a stable
// identifier it can compare, and Hint says what to do next. Both are optional —
// an HTTPError without them serializes exactly as it did before they existed.
type HTTPError struct {
	Status int
	Msg    string
	// Code is a stable, machine-readable identifier for this failure
	// (e.g. "approval_timeout"). Renaming one is a breaking API change.
	Code string
	// Hint tells the caller what to do next, in the imperative. It is aimed at
	// an agent deciding whether to retry, give up, or ask the user for help.
	Hint  string
	Extra map[string]any
}

func (e *HTTPError) Error() string { return e.Msg }

// Errorf builds an HTTPError with the given status and formatted message.
func Errorf(status int, format string, a ...any) *HTTPError {
	return &HTTPError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// WithHint attaches a stable code and a next-action hint, and returns the same
// error so it can be chained onto Errorf.
func (e *HTTPError) WithHint(code, hint string) *HTTPError {
	e.Code, e.Hint = code, hint
	return e
}

// WriteJSON serializes data as compact JSON (HTML escaping disabled, via
// jsonx.Marshal — the single shared encoder) and writes it with the given status.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	b, err := jsonx.Marshal(data)
	if err != nil {
		b := []byte(`{"error":"encode failed"}`)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(b)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// WriteError maps an error to a JSON response. An *HTTPError uses its status and
// merges its Extra fields; any other error becomes a 400.
//
// `code` and `hint` are emitted only when set, so an error that carries neither
// produces the same {"error": …} body it always has — existing clients (the tool
// pages read data.error via shared/net.js) see no change.
func WriteError(w http.ResponseWriter, err error) {
	var he *HTTPError
	if errors.As(err, &he) {
		m := map[string]any{"error": he.Msg}
		maps.Copy(m, he.Extra)
		// After Extra, so the typed fields win if a caller set both.
		if he.Code != "" {
			m["code"] = he.Code
		}
		if he.Hint != "" {
			m["hint"] = he.Hint
		}
		WriteJSON(w, he.Status, m)
		return
	}
	WriteJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
}
