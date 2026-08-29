package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
)

// runStatus reports what listens on the configured port. Exit 0 when a devhub
// (or at least a listener) is there, 1 when nothing is — so scripts can use
// `devhub status` as a liveness check.
func runStatus() int {
	store := openStoreQuiet()
	if store != nil {
		defer store.Close()
	}
	port := resolvePort(store)
	fmt.Printf("port    : %d\n", port)
	pids := listenersOn(port)
	if len(pids) == 0 {
		fmt.Println("server  : not running")
		return 1
	}
	fmt.Printf("listener: pid %s\n", joinInts(pids))
	if info, err := probeInfo(port); err == nil {
		fmt.Printf("server  : devhub %s (edition %s)\n", info.Version, info.Edition)
		fmt.Printf("home    : %s\n", info.Base)
	} else {
		fmt.Printf("server  : listener did not identify as devhub (%v)\n", err)
	}
	return 0
}

// runStop stops the devhub instance on the configured port. The listener must
// identify itself via /ai-api/info before anything is signalled — `devhub
// stop` must never become a generic port killer (the ports tool exists for
// that, with its own safety checks). Stopping when nothing runs is a no-op
// success so the command is idempotent in scripts.
func runStop() int {
	store := openStoreQuiet()
	if store != nil {
		defer store.Close()
	}
	port := resolvePort(store)
	pids := listenersOn(port)
	if len(pids) == 0 {
		fmt.Printf("no devhub listening on :%d\n", port)
		return 0
	}
	info, err := probeInfo(port)
	if err != nil {
		fmt.Fprintf(os.Stderr, "devhub: refusing to kill pid %s on :%d — %v\n", joinInts(pids), port, err)
		fmt.Fprintln(os.Stderr, "devhub: if it really must die, use the ports tool (or taskkill/kill) explicitly")
		return 1
	}
	pid, ok := verifiedListenerPID(pids, info.PID)
	if !ok {
		fmt.Fprintf(os.Stderr, "devhub: refusing to kill on :%d — identified pid %d is not a current listener (listeners: %s)\n", port, info.PID, joinInts(pids))
		return 1
	}
	if err := portsctl.KillPID(pid); err != nil {
		fmt.Fprintf(os.Stderr, "devhub: kill pid %d: %v\n", pid, err)
		return 1
	}
	// Confirm the authenticated PID's listening socket disappears. Another
	// address family may legitimately have an unrelated listener on the same
	// numeric port, and must neither be killed nor make this stop look failed.
	for range 10 {
		if !containsInt(listenersOn(port), info.PID) {
			fmt.Printf("stopped devhub %s (pid %d) on :%d\n", info.Version, info.PID, port)
			return 0
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "devhub: pid %d was signalled but is still listening on :%d\n", info.PID, port)
	return 1
}

func verifiedListenerPID(listeners []int, claimed int) (int, bool) {
	if claimed <= 0 || !containsInt(listeners, claimed) {
		return 0, false
	}
	return claimed, true
}

func containsInt(ns []int, target int) bool {
	for _, n := range ns {
		if n == target {
			return true
		}
	}
	return false
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}
