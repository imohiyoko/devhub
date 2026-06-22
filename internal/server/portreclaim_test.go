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

// reclaim must never kill the current process, even when it is the one holding
// the port: the pid==self branch makes it a no-op (returns 0). The non-self
// "foreign holder" guard is covered separately by
// TestIsDevhubProcessRejectsForeignProcess.
func TestReclaimSkipsSelfListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a probe listener: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if pid := reclaimStaleDevhubPort(port); pid != 0 {
		t.Fatalf("reclaimStaleDevhubPort(self-held %d) = %d; want 0", port, pid)
	}
}

// A real, separate process whose name is not "devhub" must fail the guard, so
// reclaim would never target it. This exercises the lsof-independent ps path of
// the guard against an actual foreign PID.
func TestIsDevhubProcessRejectsForeignProcess(t *testing.T) {
	cmd := exec.Command("sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start probe process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	if isDevhubProcess(cmd.Process.Pid) {
		t.Fatalf("isDevhubProcess(sleep pid %d) = true; want false", cmd.Process.Pid)
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
