package tools

import (
	"net/http"

	containersctl "github.com/imohiyoko/devhub/internal/controllers/containers"
	"github.com/imohiyoko/devhub/internal/core"
)

// containersTool adapts the containers controller: /api/containers (list),
// /api/containers/profiles* (create, resize, start and stop a Colima VM),
// /api/containers/{logs,stop,start,restart} (operate on one container), plus
// the /containers page.
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

func (t containersTool) control(w http.ResponseWriter, r *http.Request) error {
	data, err := decodeBody(w, r)
	if err != nil {
		return err
	}
	return t.ctl.HandleControlPost(w, r, data)
}

func (t containersTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/containers", Handle: t.ctl.HandleGet},
		// Prefix, because the per-profile routes carry the profile name in the
		// path. A POST is also what puts these behind /ai-api's approval gate.
		{Method: http.MethodPost, Pattern: "/api/containers/profiles", Prefix: true, Handle: func(w http.ResponseWriter, r *http.Request) error {
			data, err := decodeBody(w, r)
			if err != nil {
				return err
			}
			return t.ctl.HandleProfilePost(w, r, data)
		}},
		// Exact patterns, not a prefix: the container and source are in the
		// body, so there is nothing in the path to carry and no reason to
		// accept anything longer.
		{Method: http.MethodPost, Pattern: "/api/containers/logs", Handle: t.control},
		{Method: http.MethodPost, Pattern: "/api/containers/stop", Handle: t.control},
		{Method: http.MethodPost, Pattern: "/api/containers/start", Handle: t.control},
		{Method: http.MethodPost, Pattern: "/api/containers/restart", Handle: t.control},
	}
}
