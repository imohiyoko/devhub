// Package jsonx holds the one JSON encoder shared across devhub. Marshal
// encodes a value with HTML escaping disabled — so <, >, and & round-trip
// byte-for-byte (git diff output, URLs with query strings, …) — and trims the
// trailing newline encoding/json's Encoder appends, yielding the exact byte
// shape devhub persists and serves. It deliberately imports nothing from this
// repository so any package (storage, httpx, controllers) can depend on it
// without risking an import cycle.
package jsonx

import (
	"bytes"
	"encoding/json"
)

// Marshal encodes v as JSON with HTML escaping disabled and the trailing
// newline trimmed. It is byte-identical to json.Marshal except that <, >, and &
// are left unescaped.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
