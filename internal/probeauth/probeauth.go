// Package probeauth authenticates the minimal, credential-free instance probe
// used by devhub status/stop/doctor. The shared secret signs responses but is
// never sent to the listener being probed.
package probeauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

const domain = "devhub-instance-probe-v1"

// Info is the minimal process identity returned by /ai-api/probe.
type Info struct {
	Version string `json:"version"`
	Edition string `json:"edition"`
	PID     int    `json:"pid"`
	Proof   string `json:"proof"`
}

// NewNonce returns a fresh 256-bit challenge.
func NewNonce() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidNonce reports whether nonce is a canonical 256-bit challenge.
func ValidNonce(nonce string) bool {
	b, err := base64.RawURLEncoding.DecodeString(nonce)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == nonce
}

// Sign returns a response proof bound to the challenge, target port, and every
// returned identity field. Info.Proof is deliberately excluded.
func Sign(token, nonce string, port int, info Info) string {
	payload, _ := json.Marshal(struct {
		Domain  string `json:"domain"`
		Nonce   string `json:"nonce"`
		Port    int    `json:"port"`
		Version string `json:"version"`
		Edition string `json:"edition"`
		PID     int    `json:"pid"`
	}{domain, nonce, port, info.Version, info.Edition, info.PID})
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a response proof in constant time.
func Verify(token, nonce string, port int, info Info) bool {
	want, err := base64.RawURLEncoding.DecodeString(Sign(token, nonce, port, info))
	if err != nil {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(info.Proof)
	return err == nil && hmac.Equal(got, want)
}
