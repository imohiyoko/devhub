package tools

import (
	"net/http"

	logsctl "github.com/imohiyoko/devhub/internal/controllers/logs"
	"github.com/imohiyoko/devhub/internal/core"
)

// logsTool adapts the request-log controller: /api/logs (search) plus archive
// and clear, and the /logs page.
type logsTool struct{ ctl *logsctl.Controller }

func newLogs(ctl *logsctl.Controller) core.Tool { return logsTool{ctl: ctl} }

func (t logsTool) Meta() core.Meta {
	return core.Meta{
		ID:    "logs",
		Title: "logs",
		Icon:  "▤",
		Desc:  "この起動中に処理した API リクエストの記録。承認の結果で絞り込め、残したい分だけアーカイブできる",
		Page:  "tools/logs/index.html",
	}
}

func (t logsTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/logs", Handle: t.ctl.HandleGet},
		{Method: http.MethodPost, Pattern: "/api/logs/archive", Handle: t.ctl.HandleArchive},
		{Method: http.MethodPost, Pattern: "/api/logs/clear", Handle: t.ctl.HandleClear},
	}
}
