package main

import (
	"net"
	"net/http"
	"strconv"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/probeauth"
	"github.com/imohiyoko/devhub/internal/storage"
)

// TestRunSubcommandDispatch pins the non-blocking dispatch outcomes: help and
// version succeed, and an unknown word errors (exit 2) instead of falling
// through to a server start — the core guarantee of ADR 0002 decision 5 and
// 0003 (a reflexive/typo'd `devhub` never launches a server). `start` is
// intentionally excluded: it blocks in srv.Run.
func TestRunSubcommandDispatch(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"help", 0},
		{"version", 0},
		{"stpo", 2}, // typo of `stop` must not start a server
	}
	for _, c := range cases {
		if got := runSubcommand(c.name, nil); got != c.want {
			t.Errorf("runSubcommand(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestProbeInfoDoesNotSendAgentTokenToUnverifiedListener(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DEVHUB_HOME", home)
	st, err := storage.Open(home, devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.AgentToken()
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var gotAgentToken, gotNonce, gotPath string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgentToken = r.Header.Get("X-Devhub-Agent-Token")
		gotNonce = r.Header.Get("X-Devhub-Probe-Nonce")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"fake","edition":"code","pid":1234,"proof":"forged"}`))
	})}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()

	_, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probeInfo(port); err == nil {
		t.Fatal("forged listener response was accepted")
	}
	if gotAgentToken != "" {
		t.Fatal("probe disclosed X-Devhub-Agent-Token to an unverified listener")
	}
	if gotPath != "/ai-api/probe" || !probeauth.ValidNonce(gotNonce) {
		t.Fatalf("probe request path=%q nonce=%q", gotPath, gotNonce)
	}
}

func TestVerifiedListenerPIDSelectsOnlyServerClaim(t *testing.T) {
	listeners := []int{101, 202} // e.g. unrelated ::1 listener + verified IPv4 devhub
	if got, ok := verifiedListenerPID(listeners, 202); !ok || got != 202 {
		t.Fatalf("verifiedListenerPID = (%d, %v), want (202, true)", got, ok)
	}
	for _, claimed := range []int{0, 303} {
		if got, ok := verifiedListenerPID(listeners, claimed); ok || got != 0 {
			t.Errorf("claim %d = (%d, %v), want refusal", claimed, got, ok)
		}
	}
}
