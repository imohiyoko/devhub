package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
)

// generateToken returns a 32-byte URL-safe random token.
func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic; surface by panicking at startup.
		panic("devhub: failed to read CSPRNG: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// hostAllowed implements the Host-header allowlist (DNS-rebinding defense).
func (s *Server) hostAllowed(r *http.Request) bool {
	return s.allowedHosts[toLowerASCII(r.Host)]
}

// apiAuthorized enforces (C) Sec-Fetch-Site (if present must be same-origin/none)
// and (B) the constant-time token comparison.
func (s *Server) apiAuthorized(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return false
	}
	got := r.Header.Get("X-Devhub-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
