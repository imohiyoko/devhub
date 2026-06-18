package tools

import (
	"net/http"

	databasectl "github.com/imohiyoko/devhub/internal/controllers/database"
	"github.com/imohiyoko/devhub/internal/core"
)

// databaseTool adapts the db-table controller. Its page is /db-table while its
// API lives under /api/db/* — routes are declared explicitly, not derived from
// the ID, so the two can legitimately differ.
type databaseTool struct{ ctl *databasectl.Controller }

func newDatabase(ctl *databasectl.Controller) core.Tool { return databaseTool{ctl: ctl} }

func (t databaseTool) Meta() core.Meta {
	return core.Meta{
		ID:    "db-table",
		Title: "db-table",
		Icon:  "▦",
		Desc:  "SQLite / MySQL / MariaDB の接続管理、表表示、TSV/CSVコピー、列コピー、セル編集",
		Page:  "tools/db-table/index.html",
	}
}

func (t databaseTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/db/tables", Handle: t.ctl.HandleGet},
		{Method: http.MethodGet, Pattern: "/api/db/rows", Handle: t.ctl.HandleGet},
		{Method: http.MethodPost, Pattern: "/api/db/", Prefix: true, Handle: func(w http.ResponseWriter, r *http.Request) error {
			return t.ctl.HandlePost(w, r, decodeBody(w, r))
		}},
	}
}
