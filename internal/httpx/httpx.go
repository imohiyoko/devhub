// Package httpx provides the shared JSON response writer and HTTP error type
// used by the server and all controllers. WriteJSON emits UTF-8 JSON without
// HTML escaping of <, >, & so values such as git diff output round-trip
// byte-for-byte.
package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
)

// HTTPError carries an explicit status code (and optional extra fields) for an
// API error. Controllers return it to signal a specific status; any other error
// is treated as a 400 by WriteError.
type HTTPError struct {
	Status int
	Msg    string
	Extra  map[string]any
}

func (e *HTTPError) Error() string { return e.Msg }

// Errorf builds an HTTPError with the given status and formatted message.
func Errorf(status int, format string, a ...any) *HTTPError {
	return &HTTPError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// WriteJSON serializes data as compact JSON (HTML escaping disabled) and writes
// it with the given status.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		b := []byte(`{"error":"encode failed"}`)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(b)
		return
	}
	b := bytes.TrimRight(buf.Bytes(), "\n")
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// WriteError maps an error to a JSON response. An *HTTPError uses its status and
// merges its Extra fields; any other error becomes a 400.
func WriteError(w http.ResponseWriter, err error) {
	var he *HTTPError
	if errors.As(err, &he) {
		m := map[string]any{"error": he.Msg}
		maps.Copy(m, he.Extra)
		WriteJSON(w, he.Status, m)
		return
	}
	WriteJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
}
