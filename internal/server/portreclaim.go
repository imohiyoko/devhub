package server

import (
	"os"

	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
)

// reclaimStaleDevhubPort frees the given TCP port by terminating a *devhub*
// process that is still listening on it — typically an orphan left behind by a
// previous rebuild (for example the compiled child of `go run` whose parent was
// killed), or on Windows simply the previous instance (there is no exec-based
// restart there). It only ever signals a process whose executable basename is
// exactly "devhub" and which is not the current process, so an unrelated
// application that happens to hold the port is never touched. Returns the PID
// it killed, or 0 when there was nothing safe to reclaim.
//
// The guard is by name only, not by liveness: an orphaned rebuild child is a
// fully functional, responsive devhub, so there is no signal that distinguishes
// it from an "intentional" instance. This therefore takes over a healthy but
// parentless devhub as well — which is the intended semantics, since devhub is
// single-instance per fixed port and "newest launch wins". The caller logs the
// reclaim to stderr so the takeover is visible.
//
// Listener discovery reuses the ports tool's listing (lsof on unix, netstat on
// Windows); the name check is per-OS (ps / tasklist). Where those tools are
// absent this yields nothing and stays a no-op, leaving the caller's plain
// bind-retry behaviour unchanged.
func reclaimStaleDevhubPort(port int) int {
	self := os.Getpid()
	for _, pid := range listenerPIDs(port) {
		if pid == self || !isDevhubProcess(pid) {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		// Kill, not the SIGTERM that internal/controllers/ports uses: the
		// holder is by definition not winding down on its own, and devhub
		// installs no graceful-shutdown handler, so a hard kill loses nothing
		// and releases the LISTEN socket promptly.
		if proc.Kill() == nil {
			return pid
		}
	}
	return 0
}

// listenerPIDs returns the PIDs holding a LISTEN socket on the given TCP port.
func listenerPIDs(port int) []int {
	entries, err := portsctl.ListListening()
	if err != nil {
		return nil
	}
	var pids []int
	seen := map[int]bool{}
	for _, e := range entries {
		if e.Port == port && !seen[e.PID] {
			seen[e.PID] = true
			pids = append(pids, e.PID)
		}
	}
	return pids
}
