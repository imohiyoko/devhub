package server

import (
	"net/http"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
)

// handleRestart serves POST /api/restart. It acknowledges immediately, then
// re-execs the binary, carrying the current token in DEVHUB_API_TOKEN so tabs
// holding the injected token stay authorized after the restart.
func (s *Server) handleRestart(w http.ResponseWriter, _ *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	token := s.token
	go func() {
		time.Sleep(300 * time.Millisecond)
		reexec(token)
	}()
	return nil
}
