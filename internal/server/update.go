package server

import (
	"context"
	"net/http"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/platform"
	"github.com/imohiyoko/devhub/internal/updater"
)

// handleUpdateStatus serves GET /api/update/status. It reports whether a newer
// release exists and how to move to it (one-click for the installer edition, a
// manual command otherwise). The check is off when update_check is disabled in
// settings; a source/dev run never reaches the network (see updater.Status).
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) error {
	if !s.updateCheckEnabled() {
		httpx.WriteJSON(w, http.StatusOK, updater.Status{
			Current: s.version, Edition: s.edition, Disabled: true,
		})
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	httpx.WriteJSON(w, http.StatusOK, updater.Check(ctx, s.version, s.edition))
	return nil
}

// handleUpdateApply serves POST /api/update/apply. It downloads and verifies the
// latest release (same checks as the install scripts) and swaps in the new
// binary, then re-execs so the replacement takes over — the frontend detects the
// new process via /api/info's instance id (the rebuild flow's mechanism). Only
// the installer edition can self-update; other editions get a 409 with guidance.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) error {
	if s.edition != platform.EditionInstaller {
		return httpx.Errorf(http.StatusConflict,
			"この配布形態（%s）では自己更新できません。%s", s.edition, updateHintFor(s.edition))
	}

	// A generous ceiling for download+verify; independent of the client request
	// so a browser giving up mid-flight can't abort a partially-applied update.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rel, err := updater.Latest(ctx)
	if err != nil {
		return httpx.Errorf(http.StatusBadGateway, "最新版の取得に失敗しました: %v", err)
	}
	if !updater.IsNewer(rel.Tag, s.version) {
		return httpx.Errorf(http.StatusConflict, "既に最新です（%s）", s.version)
	}
	if err := updater.SelfUpdate(ctx, rel.Tag); err != nil {
		return httpx.Errorf(http.StatusInternalServerError, "更新に失敗しました: %v", err)
	}

	// Success: acknowledge, flush, then re-exec into the freshly-installed binary.
	// The short delay lets the response reach the browser before the process is
	// replaced (mirrors handleRestart).
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "restarting": true, "version": rel.Tag,
	})
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

// updateHintFor returns the manual upgrade command for a non-installer edition,
// used in the 409 message when self-update does not apply.
func updateHintFor(edition string) string {
	switch edition {
	case platform.EditionHomebrew:
		return "更新は `brew upgrade --cask devhub` を実行してください。"
	default:
		return "ソースから起動しているため更新は不要です。"
	}
}

// updateCheckEnabled reads the live update_check setting from the store so a
// toggle takes effect without a restart. Absent or unset means enabled — the
// check is opt-out, not opt-in.
func (s *Server) updateCheckEnabled() bool {
	st, err := s.store.LoadSettings()
	if err != nil {
		return true
	}
	if v, ok := st["update_check"].(bool); ok {
		return v
	}
	return true
}
