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
	pids, err := listenersOn(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "devhub: listing ports:", err)
		return 1
	}
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
// identify itself via a signed /ai-api/probe before anything is signalled — `devhub
// stop` must never become a generic port killer (the ports tool exists for
// that, with its own safety checks). Stopping when nothing runs is a no-op
// success so the command is idempotent in scripts.
func runStop() int {
	store := openStoreQuiet()
	if store != nil {
		defer store.Close()
	}
	ports := resolvePortCandidates(store)
	refused := 0
	for _, port := range ports {
		pids, err := listenersOn(port)
		if err != nil {
			fmt.Fprintln(os.Stderr, "devhub: listing ports:", err)
			return 1
		}
		if len(pids) == 0 {
			continue
		}
		info, err := probeInfo(port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "devhub: refusing to kill pid %s on :%d — %v\n", joinInts(pids), port, err)
			refused++
			continue
		}
		pid, ok := verifiedListenerPID(pids, info.PID)
		if !ok {
			fmt.Fprintf(os.Stderr, "devhub: refusing to kill on :%d — identified pid %d is not a current listener (listeners: %s)\n", port, info.PID, joinInts(pids))
			refused++
			continue
		}
		if err := portsctl.KillPID(pid); err != nil {
			fmt.Fprintf(os.Stderr, "devhub: kill pid %d: %v\n", pid, err)
			refused++
			continue
		}
		exited, err := waitForListenerExit(port, pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "devhub: pid %d was signalled but listener exit could not be verified on :%d: %v\n", pid, port, err)
			refused++
			continue
		}
		if exited {
			fmt.Printf("stopped devhub %s (pid %d) on :%d\n", info.Version, pid, port)
			if store != nil {
				if active, err := store.LoadActiveInstance(); err == nil && active.Port == port && active.PID == pid {
					if err := store.ClearActiveInstance(active); err != nil {
						fmt.Fprintf(os.Stderr, "devhub: clear active instance: %v\n", err)
					}
				}
			}
			// Candidate ports describe alternative locations for one logical
			// main instance, not a group to terminate together.
			return 0
		} else {
			fmt.Fprintf(os.Stderr, "devhub: pid %d was signalled but is still listening on :%d\n", pid, port)
			refused++
		}
	}
	if refused > 0 {
		fmt.Fprintln(os.Stderr, "devhub: if it really must die, use the ports tool (or taskkill/kill) explicitly")
		return 1
	}
	fmt.Printf("no devhub listening on %s\n", joinPorts(ports))
	return 0
}

func waitForListenerExit(port, pid int) (bool, error) {
	return waitForListenerExitWith(port, pid, listenersOn)
}

func waitForListenerExitWith(port, pid int, lookup func(int) ([]int, error)) (bool, error) {
	for range 10 {
		pids, err := lookup(port)
		if err != nil {
			return false, err
		}
		if !containsInt(pids, pid) {
			return true, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false, nil
}

func joinPorts(ports []int) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = fmt.Sprintf(":%d", port)
	}
	return strings.Join(parts, ", ")
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
