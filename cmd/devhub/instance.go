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
	failed := false
	for _, pid := range pids {
		if err := portsctl.KillPID(pid); err != nil {
			fmt.Fprintf(os.Stderr, "devhub: kill pid %d: %v\n", pid, err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	// Confirm the port actually frees: SIGTERM is delivered asynchronously and
	// a lingering listener means "stopped" would be a lie. devhub installs no
	// graceful-shutdown handler, so a healthy stop clears within a beat.
	for range 10 {
		if len(listenersOn(port)) == 0 {
			fmt.Printf("stopped devhub %s (pid %s) on :%d\n", info.Version, joinInts(pids), port)
			return 0
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "devhub: pid %s was signalled but :%d is still listening\n", joinInts(pids), port)
	return 1
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}
