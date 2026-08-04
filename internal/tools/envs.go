package tools

import (
	"net/http"

	envsctl "github.com/imohiyoko/devhub/internal/controllers/envs"
	"github.com/imohiyoko/devhub/internal/core"
)

// envsTool adapts the env-launcher controller. Page is /env-launcher; API is
// /api/envs and /api/envs/* (launch, launches, worktrees, state, switch).
type envsTool struct{ ctl *envsctl.Controller }

func newEnvs(ctl *envsctl.Controller) core.Tool { return envsTool{ctl: ctl} }

func (t envsTool) Meta() core.Meta {
	return core.Meta{
		ID:    "env-launcher",
		Title: "env launcher",
		Icon:  "🚀",
		Desc:  "検証環境の起動・管理",
		Page:  "tools/env-launcher/index.html",
	}
}

func (t envsTool) Routes() []core.Route {
	post := func(w http.ResponseWriter, r *http.Request) error {
		return t.ctl.HandlePost(w, r, decodeBody(w, r))
	}
	return []core.Route{
		// Exact /api/envs and any /api/envs/* path both dispatch to the
		// controller, which switches on the full path internally.
		{Method: http.MethodGet, Pattern: "/api/envs", Handle: t.ctl.HandleGet},
		{Method: http.MethodGet, Pattern: "/api/envs/", Prefix: true, Handle: t.ctl.HandleGet},
		{Method: http.MethodPost, Pattern: "/api/envs", Handle: post},
		{Method: http.MethodPost, Pattern: "/api/envs/", Prefix: true, Handle: post},
	}
}
