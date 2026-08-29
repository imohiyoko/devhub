package probeauth

import "testing"

func TestProofBindsNoncePortAndIdentity(t *testing.T) {
	token := "test-shared-secret"
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidNonce(nonce) {
		t.Fatalf("generated nonce is invalid: %q", nonce)
	}
	info := Info{Version: "dev", Edition: "code", PID: 1234}
	info.Proof = Sign(token, nonce, 8765, info)
	if !Verify(token, nonce, 8765, info) {
		t.Fatal("valid proof was rejected")
	}

	mutations := []struct {
		name  string
		token string
		nonce string
		port  int
		info  Info
	}{
		{"wrong token", "other", nonce, 8765, info},
		{"replayed nonce", token, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 8765, info},
		{"other port", token, nonce, 8766, info},
		{"tampered pid", token, nonce, 8765, Info{Version: info.Version, Edition: info.Edition, PID: 9999, Proof: info.Proof}},
		{"tampered version", token, nonce, 8765, Info{Version: "other", Edition: info.Edition, PID: info.PID, Proof: info.Proof}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			if Verify(tc.token, tc.nonce, tc.port, tc.info) {
				t.Fatal("tampered proof was accepted")
			}
		})
	}
}

func TestValidNonceRejectsNonCanonicalValues(t *testing.T) {
	for _, nonce := range []string{"", "short", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="} {
		if ValidNonce(nonce) {
			t.Errorf("ValidNonce(%q) = true", nonce)
		}
	}
}
