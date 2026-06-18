package server

import (
	"net/http"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/platform"
)

// handleInfo serves GET /api/info. It returns the actually-bound port (not the
// configured one) so the frontend stays correct under DEVHUB_PORT overrides.
// `base` is the devhub home dir (the legacy install-dir hint is meaningless for
// a single binary).
func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"base":       platform.DevhubHome(),
		"port":       s.port,
		"home":       platform.Home(),
		"is_windows": platform.IsWindows(),
	})
}
