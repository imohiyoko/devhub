package server

import (
	"net/http"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/platform"
)

// instanceID is a fresh random id minted once per process start. It changes on
// every (re)start — including a rebuild that re-execs or spawns a replacement —
// so the frontend can detect "the server I'm talking to is a new process" by
// comparing /api/info's `instance` against the value it captured before the
// rebuild, instead of trying to catch the transient down-window (which a fast
// `go run` restart can slip through entirely, leaving the UI polling forever).
var instanceID = generateToken()

// handleInfo serves GET /api/info. It returns the actually-bound port (not the
// configured one) so the frontend stays correct under DEVHUB_PORT overrides.
// `base` is the devhub home dir (the legacy install-dir hint is meaningless for
// a single binary). `instance` is a per-process id used for restart detection.
func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"base":       platform.DevhubHome(),
		"port":       s.port,
		"home":       platform.Home(),
		"is_windows": platform.IsWindows(),
		"instance":   instanceID,
	})
}
