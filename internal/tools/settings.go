package tools

import (
	"net/http"

	settingsctl "github.com/imohiyoko/devhub/internal/controllers/settings"
	"github.com/imohiyoko/devhub/internal/core"
)

// settingsTool adapts the settings controller. It is API-only (no Page), so it
// has no dashboard card; the dashboard's own settings panel consumes these
// endpoints: /api/config, /api/settings, /api/settings/tool/{id}.
type settingsTool struct{ ctl *settingsctl.Controller }

func newSettings(ctl *settingsctl.Controller) core.Tool { return settingsTool{ctl: ctl} }

func (t settingsTool) Meta() core.Meta {
	return core.Meta{ID: "settings", Title: "settings"} // Page empty → not a nav card
}

func (t settingsTool) Routes() []core.Route {
	post := func(w http.ResponseWriter, r *http.Request) error {
		return t.ctl.HandlePost(w, r, decodeBody(w, r))
	}
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/config", Handle: t.ctl.HandleGet},
		{Method: http.MethodGet, Pattern: "/api/settings", Handle: t.ctl.HandleGet},
		{Method: http.MethodGet, Pattern: "/api/settings/tool/", Prefix: true, Handle: t.ctl.HandleGet},
		{Method: http.MethodPost, Pattern: "/api/config", Handle: post},
		{Method: http.MethodPost, Pattern: "/api/settings", Handle: post},
		{Method: http.MethodPost, Pattern: "/api/settings/tool/", Prefix: true, Handle: post},
	}
}
