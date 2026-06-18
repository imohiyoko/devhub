package tools

import (
	"net/http"

	gitctl "github.com/imohiyoko/devhub/internal/controllers/git"
	"github.com/imohiyoko/devhub/internal/core"
	"github.com/imohiyoko/devhub/internal/httpx"
)

// gitTool adapts the git controller to the core.Tool contract. It owns the
// /api/repos and /api/git/* routes and the /git page.
type gitTool struct{ ctl *gitctl.Controller }

func newGit(ctl *gitctl.Controller) core.Tool { return gitTool{ctl: ctl} }

func (t gitTool) Meta() core.Meta {
	return core.Meta{
		ID:    "git",
		Title: "git",
		Icon:  "⎇",
		Desc:  "lazygit風ブラウザUI。ステージング・コミット・ブランチ管理・プッシュ・プルをキーボードで操作",
		Page:  "tools/git/index.html",
	}
}

func (t gitTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/repos", Handle: func(w http.ResponseWriter, _ *http.Request) error {
			httpx.WriteJSON(w, http.StatusOK, t.ctl.AllRepos())
			return nil
		}},
		{Method: http.MethodGet, Pattern: "/api/git/", Prefix: true, Handle: t.ctl.HandleGet},
		{Method: http.MethodPost, Pattern: "/api/git/", Prefix: true, Handle: func(w http.ResponseWriter, r *http.Request) error {
			return t.ctl.HandlePost(w, r, decodeBody(w, r))
		}},
	}
}
