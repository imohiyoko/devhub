package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// reclaimStaleDevhubPort frees the given TCP port by terminating a *devhub*
// process that is still listening on it — typically an orphan left behind by a
// previous rebuild (for example the compiled child of `go run` whose parent was
// killed). It only ever signals a process whose executable basename is exactly
// "devhub" and which is not the current process, so an unrelated application
// that happens to hold the port is never touched. Returns the PID it killed, or
// 0 when there was nothing safe to reclaim.
//
// The guard is by name only, not by liveness: an orphaned rebuild child is a
// fully functional, responsive devhub, so there is no signal that distinguishes
// it from an "intentional" instance. This therefore takes over a healthy but
// parentless devhub as well — which is the intended semantics, since devhub is
// single-instance per fixed port and "newest launch wins". The caller logs the
// reclaim to stderr so the takeover is visible.
//
// Implemented for unix via lsof + ps; on platforms where those tools are absent
// listenerPIDs yields nothing and this is a no-op, leaving the caller's plain
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
		// SIGKILL, not the SIGTERM that internal/controllers/ports uses: the
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
	// -t prints just PIDs. lsof exits non-zero when some handles are
	// inaccessible but still prints the ones it can see, so stdout is parsed
	// regardless of the exit status (mirrors internal/controllers/ports).
	out, _ := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t").Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// isDevhubProcess reports whether pid's executable basename is exactly "devhub".
// This is the guard that keeps reclaim from killing an unrelated process: a
// `go run` parent reports "go", an editor or proxy reports its own name, and
// only a real devhub binary (installed or the go-run compiled child) matches.
func isDevhubProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	return filepath.Base(strings.TrimSpace(string(out))) == "devhub"
}
