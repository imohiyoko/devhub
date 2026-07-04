// Package settings implements the /api/config, /api/settings and
// /api/settings/tool/{id} endpoints.
package settings

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/imohiyoko/devhub/internal/core"
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

// Controller serves settings/config endpoints. The global settings and git
// config documents still use the typed *storage.Store (they carry seeding and
// merge logic). The per-tool settings document is reached only through the
// core.Store seam: toolSettings is a core.Namespace(store, "tool") view, so a
// GET/POST /api/settings/tool/<id> is a plain Get/Set on key "tool:<id>" — the
// same key the store already uses, so no data migration is involved.
type Controller struct {
	store        *storage.Store
	toolSettings core.Store
}

// New returns a settings controller. toolSettings is the namespaced view that
// owns the "tool:<id>" keyspace.
func New(store *storage.Store, toolSettings core.Store) *Controller {
	return &Controller{store: store, toolSettings: toolSettings}
}

func toolIDFromPath(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// marshalToolSettings encodes a tool-settings document without HTML-escaping,
// matching how the store persisted these documents before the seam migration
// (values like URLs with "&" must round-trip byte-for-byte).
func marshalToolSettings(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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
		raw, err := c.toolSettings.Get(toolID)
		if err != nil {
			return err
		}
		ts := map[string]any{}
		if raw != nil {
			_ = json.Unmarshal(raw, &ts)
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
		b, err := marshalToolSettings(data)
		if err != nil {
			return err
		}
		if err := c.toolSettings.Set(toolID, b); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	return nil
}
