//go:build !windows

package server

import "syscall"

// detachAttr puts the rebuilt process in its own session (setsid), detached from
// the controlling terminal of the process that spawned it.
//
// This matters when devhub was started via `go run` from an interactive
// terminal (e.g. scripts/dev.sh does `exec go run ./cmd/devhub start`). On rebuild the
// old process spawns the replacement and then exits; its own `go run` parent
// exits too, and the terminal emulator tears down the pty — sending SIGHUP to
// every process still in that session, which would otherwise kill the freshly
// spawned replacement before it ever binds the port. A fresh session severs that
// link so the new instance survives the terminal teardown.
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
