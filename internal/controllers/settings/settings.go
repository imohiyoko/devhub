// Package settings implements the /api/config, /api/settings and
// /api/settings/tool/{id} endpoints. Ports backend/controllers/settings.py.
package settings

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/portutil"
	"github.com/imohiyoko/devhub/internal/sanitize"
	"github.com/imohiyoko/devhub/internal/storage"
)

var toolIDRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// settingsAllowlist is the set of keys POST /api/settings will persist.
var settingsAllowlist = map[string]bool{
	"disabled_tools": true, "tool_order": true, "editor": true,
	"open_browser_on_start": true, "db_connections": true, "port_labels": true,
	"protected_ports": true, "terminal": true,
}

var configKeys = []string{"scan_roots", "excludes", "pinned_repos", "repo_order", "hidden_repos"}

// Controller serves settings/config endpoints backed by the store.
type Controller struct{ store *storage.Store }

// New returns a settings controller.
func New(store *storage.Store) *Controller { return &Controller{store: store} }

func toolIDFromPath(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// HandleGet dispatches GET config/settings/tool requests.
func (c *Controller) HandleGet(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	switch {
	case path == "/api/config":
		cfg, err := c.store.LoadConfig()
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, cfg)
	case path == "/api/settings":
		st, err := c.store.LoadSettings()
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, sanitize.Settings(st))
	case strings.HasPrefix(path, "/api/settings/tool/"):
		toolID := toolIDFromPath(path)
		if !toolIDRe.MatchString(toolID) {
			return httpx.Errorf(http.StatusBadRequest, "invalid tool_id")
		}
		ts, err := c.store.LoadToolSettings(toolID)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, ts)
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	return nil
}

// HandlePost dispatches POST config/settings/tool requests.
func (c *Controller) HandlePost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	path := r.URL.Path
	switch {
	case path == "/api/config":
		cfg, err := c.store.LoadConfig()
		if err != nil {
			return err
		}
		for _, key := range configKeys {
			if v, ok := data[key]; ok {
				cfg[key] = v
			}
		}
		if err := c.store.SaveConfig(cfg); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case path == "/api/settings":
		patch := map[string]any{}
		for k, v := range data {
			if settingsAllowlist[k] {
				patch[k] = v
			}
		}
		if conns, ok := patch["db_connections"].([]any); ok {
			san := make([]any, 0, len(conns))
			for _, c2 := range conns {
				if m, ok := c2.(map[string]any); ok {
					san = append(san, sanitize.DBConnection(m))
				}
			}
			patch["db_connections"] = san
		}
		if _, ok := patch["protected_ports"]; ok {
			pl, err := portutil.NormalizePortList(patch["protected_ports"], true)
			if err != nil {
				return err
			}
			patch["protected_ports"] = pl
		}
		if err := c.store.SaveSettings(patch); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	case strings.HasPrefix(path, "/api/settings/tool/"):
		toolID := toolIDFromPath(path)
		if !toolIDRe.MatchString(toolID) {
			return httpx.Errorf(http.StatusBadRequest, "invalid tool_id")
		}
		if err := c.store.SaveToolSettings(toolID, data); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	return nil
}
