package server

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/imohiyoko/devhub/internal/sanitize"
)

const (
	// maxApprovalBodyBytes caps how much of an /ai-api write body is read for the
	// approval preview. It mirrors the 10 MiB request-body cap the tool handlers
	// apply, so a body under that cap is restored intact for the downstream
	// handler (a larger body would be rejected downstream anyway).
	maxApprovalBodyBytes = 10 << 20
	// maxApprovalSummaryRunes bounds the preview length shown in the prompt so a
	// large payload cannot bloat the approval request or a persisted rule.
	maxApprovalSummaryRunes = 512
)

// summarizeApprovalBody renders a compact, secret-redacted, single-line preview
// of an /ai-api write body for the approval prompt. It returns "" for an empty
// body. JSON is normalized (whitespace stripped, keys sorted by json.Marshal) so
// the preview — and any always-allow rule matched against it — is deterministic.
func summarizeApprovalBody(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		// Not JSON: collapse whitespace and show a truncated raw preview.
		return truncateRunes(strings.Join(strings.Fields(string(body)), " "), maxApprovalSummaryRunes)
	}
	redactSecrets(v)
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return truncateRunes(string(b), maxApprovalSummaryRunes)
}

// redactSecrets replaces secret-bearing map values (password/token/apikey/…)
// with "***" in place, using the same key heuristic as the settings sanitizer,
// so secrets never appear in the approval prompt or in a persisted always-allow
// rule's detail pattern.
func redactSecrets(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if sanitize.IsSecretKey(k) {
				t[k] = "***"
			} else {
				redactSecrets(val)
			}
		}
	case []any:
		for _, item := range t {
			redactSecrets(item)
		}
	}
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…(truncated)"
}
