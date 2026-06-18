package tools

import (
	"net/http"

	workspacectl "github.com/imohiyoko/devhub/internal/controllers/workspace"
	"github.com/imohiyoko/devhub/internal/core"
)

// workspaceTool adapts the workspace controller: /api/ls (directory browse) and
// /api/open (open in editor), plus the /workspace page.
type workspaceTool struct{ ctl *workspacectl.Controller }

func newWorkspace(ctl *workspacectl.Controller) core.Tool { return workspaceTool{ctl: ctl} }

func (t workspaceTool) Meta() core.Meta {
	return core.Meta{
		ID:    "workspace",
		Title: "workspace",
		Icon:  "📁",
		Desc:  "リポジトリ一覧から選んで VSCode で開く",
		Page:  "tools/workspace/index.html",
	}
}

func (t workspaceTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/ls", Handle: t.ctl.HandleLs},
		{Method: http.MethodGet, Pattern: "/api/open", Handle: t.ctl.HandleOpen},
	}
}
