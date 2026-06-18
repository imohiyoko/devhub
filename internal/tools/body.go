// Package tools is the wiring layer that adapts each existing controller into a
// core.Tool and assembles the registry handed to the gateway. It is the only
// layer that imports both core and the concrete controllers; core stays a
// dependency-free kernel and the controllers stay unaware of the registry.
package tools

import (
	"encoding/json"
	"io"
	"net/http"
)

// maxBodyBytes caps a POST body so a malformed/oversized request can't exhaust
// memory. 10 MiB is generous for the local JSON payloads this server handles.
// (Mirrors the cap the legacy router applied.)
const maxBodyBytes = 10 << 20

// decodeBody reads and JSON-decodes a request body into a map, applying the
// size cap. A bad or empty body yields an empty map (not an error), matching
// the legacy router's lenient behavior so POST handlers see the same input.
func decodeBody(w http.ResponseWriter, r *http.Request) map[string]any {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, _ := io.ReadAll(r.Body)
	data := map[string]any{}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &data)
	}
	return data
}
