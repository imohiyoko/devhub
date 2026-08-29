package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"testing"

	devhub "github.com/imohiyoko/devhub"
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

func TestProbeInfoSendsSameUserToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DEVHUB_HOME", home)
	st, err := storage.Open(home, devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, err := st.AgentToken()
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Devhub-Agent-Token"); got != token {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"version":"test","instance":"instance-1","pid":1234}`)
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
	info, err := probeInfo(port)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "test" || info.Instance != "instance-1" || info.PID != 1234 {
		t.Fatalf("info = %#v", info)
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
