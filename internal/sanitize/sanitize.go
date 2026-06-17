// Package sanitize strips secret-bearing keys from settings and DB connection
// profiles before they are returned to the frontend. Ports backend/controllers/base.py.
package sanitize

import (
	"maps"
	"strings"
)

// secretKeys are matched case-insensitively as substrings of a key name. The
// list is lowercased (the Python source lowercases the key but kept mixed-case
// needles, so "apiKey" slipped through); lowercasing both sides matches the
// clear intent — a case-insensitive partial match.
var secretKeys = []string{"password", "secret", "apikey", "api_key", "api-key", "token"}

// IsSecretKey reports whether a key name looks like it carries a secret.
func IsSecretKey(k string) bool {
	lower := strings.ToLower(k)
	for _, s := range secretKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// DBConnection returns a copy of profile with secret-bearing keys removed.
func DBConnection(profile map[string]any) map[string]any {
	out := make(map[string]any, len(profile))
	for k, v := range profile {
		if !IsSecretKey(k) {
			out[k] = v
		}
	}
	return out
}

// Settings returns a shallow copy of settings with each db_connections entry
// sanitized.
func Settings(settings map[string]any) map[string]any {
	out := make(map[string]any, len(settings))
	maps.Copy(out, settings)
	if conns, ok := out["db_connections"].([]any); ok {
		san := make([]any, 0, len(conns))
		for _, c := range conns {
			if m, ok := c.(map[string]any); ok {
				san = append(san, DBConnection(m))
			}
		}
		out["db_connections"] = san
	}
	return out
}
