//go:build windows

package server

import (
	"os/exec"
	"strconv"
	"strings"
)

// isDevhubProcess reports whether pid's image name is exactly "devhub.exe"
// (same guard as the unix ps check; see portreclaim_unix.go). Without this
// Windows implementation reclaim was a silent no-op, so a new `devhub` could
// never take the port over — the old instance survived and kept serving stale
// code with no visible reason.
func isDevhubProcess(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output() //execaudit:portreclaim
	if err != nil {
		return false
	}
	return tasklistImageName(string(out)) == "devhub"
}

// tasklistImageName extracts the image name (lowercased, .exe stripped) from
// `tasklist /NH /FO CSV` output. A pid with no match yields an INFO: line
// instead of CSV — that returns "" and fails the guard closed.
func tasklistImageName(out string) string {
	line := strings.TrimSpace(out)
	if !strings.HasPrefix(line, `"`) {
		return ""
	}
	name, _, ok := strings.Cut(strings.TrimPrefix(line, `"`), `"`)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(name), ".exe")
}
