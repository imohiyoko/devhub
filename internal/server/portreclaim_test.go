package server

import (
	"net"
	"os"
	"os/exec"
	"testing"
)

// The reclaim guard must never flag the current process: the test binary is
// named like "server.test", not "devhub", so isDevhubProcess(self) is false.
// This is what stops reclaim from killing an unrelated holder of the port.
func TestIsDevhubProcessRejectsSelf(t *testing.T) {
	if isDevhubProcess(os.Getpid()) {
		t.Fatal("isDevhubProcess(self) = true; the guard would allow killing a non-devhub process")
	}
}

// With a real (non-devhub) listener on the port, reclaim must refuse to kill it
// and return 0. This exercises the full path (lsof + ps + guard) on platforms
// where lsof is present; where it is absent the loop is simply empty and the
// result is still 0.
func TestReclaimSpareNonDevhubListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a probe listener: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if pid := reclaimStaleDevhubPort(port); pid != 0 {
		t.Fatalf("reclaimStaleDevhubPort(%d) = %d; want 0 (must not kill a non-devhub listener)", port, pid)
	}
	// The probe listener must still be alive afterwards.
	if err := ln.Close(); err != nil {
		// Already closed by us via defer only at function end, so a failure here
		// would indicate the socket was torn down unexpectedly.
		t.Fatalf("probe listener was disturbed: %v", err)
	}
}

// goBin must always return a usable path: the one on PATH when present, and a
// non-empty fallback otherwise.
func TestGoBinResolvesToPathWhenAvailable(t *testing.T) {
	got := goBin()
	if got == "" {
		t.Fatal("goBin() returned an empty string")
	}
	if p, err := exec.LookPath("go"); err == nil && got != p {
		t.Fatalf("goBin() = %q; want PATH-resolved %q", got, p)
	}
}
