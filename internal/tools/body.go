// Package tools is the wiring layer that adapts each existing controller into a
// core.Tool and assembles the registry handed to the gateway. It is the only
// layer that imports both core and the concrete controllers; core stays a
// dependency-free kernel and the controllers stay unaware of the registry.
package tools

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/imohiyoko/devhub/internal/httpx"
)

// maxBodyBytes caps a POST body so a malformed/oversized request can't exhaust
// memory. 10 MiB is generous for the local JSON payloads this server handles.
// (Mirrors the cap the legacy router applied.)
const maxBodyBytes = 10 << 20

// decodeBody reads and JSON-decodes a request body into a map, applying the
// size cap. Invalid, empty, oversized and non-object JSON are rejected at this
// shared boundary so a handler can never reinterpret a parse failure as an
// empty document (which is destructive for full-document replace endpoints).
func decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, httpx.Errorf(http.StatusRequestEntityTooLarge, "request body exceeds %d bytes", maxBodyBytes)
		}
		if errors.Is(err, io.EOF) {
			return nil, httpx.Errorf(http.StatusBadRequest, "request body is required")
		}
		return nil, httpx.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	if data == nil {
		return nil, httpx.Errorf(http.StatusBadRequest, "request body must be a JSON object")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, httpx.Errorf(http.StatusRequestEntityTooLarge, "request body exceeds %d bytes", maxBodyBytes)
		}
		return nil, httpx.Errorf(http.StatusBadRequest, "request body must contain exactly one JSON object")
	}
	return data, nil
}
