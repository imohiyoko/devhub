package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
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

// isLoopback checks if the request originates from a loopback address.
func (s *Server) isLoopback(r *http.Request) bool {
	// RemoteAddr has the IP:port of the client.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// On Windows or dual-stack IPv4/IPv6 loopback might be ::1 or 127.0.0.1.
	if host == "::1" || host == "127.0.0.1" {
		return true
	}
	// For other representations, try parsing.
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

