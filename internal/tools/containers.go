package tools

import (
	"net/http"

	containersctl "github.com/imohiyoko/devhub/internal/controllers/containers"
	"github.com/imohiyoko/devhub/internal/core"
)

// containersTool adapts the containers controller: /api/containers (list), plus
// the /containers page. Read-only for now — the panel's operations land with
// their own routes and their own execaudit Surface.
type containersTool struct{ ctl *containersctl.Controller }

func newContainers(ctl *containersctl.Controller) core.Tool { return containersTool{ctl: ctl} }

func (t containersTool) Meta() core.Meta {
	return core.Meta{
		ID:    "containers",
		Title: "containers",
		Icon:  "▤",
		Desc:  "Docker context と Colima profile を横断したコンテナ一覧。宣言されていないコンテナも見える",
		Page:  "tools/containers/index.html",
	}
}

func (t containersTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/containers", Handle: t.ctl.HandleGet},
	}
}
