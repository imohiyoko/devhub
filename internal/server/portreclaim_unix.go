//go:build !windows

package server

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// isDevhubProcess reports whether pid's executable basename is exactly "devhub".
// This is the guard that keeps reclaim from killing an unrelated process: a
// `go run` parent reports "go", an editor or proxy reports its own name, and
// only a real devhub binary (installed or the go-run compiled child) matches.
func isDevhubProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() //execaudit:portreclaim
	if err != nil {
		return false
	}
	return filepath.Base(strings.TrimSpace(string(out))) == "devhub"
}
