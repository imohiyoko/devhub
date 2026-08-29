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

// sameOriginOrNonBrowser reports whether a request is safe from a cross-site
// browser's perspective: it carries no Sec-Fetch-Site (a non-browser client such
// as curl or a local agent) or that header marks the navigation as same-origin/
// none. A same-site or cross-site browser request is rejected. This is the
// shared CSRF / DNS-rebinding guard for both credential-gated API surfaces.
func sameOriginOrNonBrowser(r *http.Request) bool {
	sfs := r.Header.Get("Sec-Fetch-Site")
	return sfs == "" || sfs == "same-origin" || sfs == "none"
}

// apiAuthorized enforces (C) the Sec-Fetch-Site guard and (B) the constant-time
// token comparison.
func (s *Server) apiAuthorized(r *http.Request) bool {
	if !sameOriginOrNonBrowser(r) {
		return false
	}
	got := r.Header.Get("X-Devhub-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// agentAuthorized authenticates the non-browser /ai-api surface.
// The credential lives in a mode-0600 file under DEVHUB_HOME so a local process
// running as another OS user cannot use devhub as a confused deputy to read the
// owner's environment definitions, Git data or SQLite files.
func (s *Server) agentAuthorized(r *http.Request) bool {
	got := r.Header.Get("X-Devhub-Agent-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.agentToken)) == 1
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
