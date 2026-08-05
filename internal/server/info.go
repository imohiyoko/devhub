package server

import (
	"net/http"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/platform"
)

// handleInfo serves GET /api/info. It returns the actually-bound port (not the
// configured one) so the frontend stays correct under DEVHUB_PORT overrides.
// `base` is the devhub home dir (the legacy install-dir hint is meaningless for
// a single binary). `instance` identifies this server and drives the frontend's
// restart detection; see the field's comment in server.go.
func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	// Normalize to a non-nil slice so the field is always a JSON array, never
	// null — the dashboard treats a non-empty array as "show the warning".
	warnings := s.store.MigrationWarnings()
	if warnings == nil {
		warnings = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"base":               platform.DevhubHome(),
		"port":               s.port,
		"home":               platform.Home(),
		"is_windows":         platform.IsWindows(),
		"system":             platform.SystemName(),
		"instance":           s.instance,
		"version":            s.version,
		"edition":            s.edition,
		"migration_warnings": warnings,
	})
}
