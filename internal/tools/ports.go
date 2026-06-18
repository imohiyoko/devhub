package tools

import (
	"net/http"

	portsctl "github.com/imohiyoko/devhub/internal/controllers/ports"
	"github.com/imohiyoko/devhub/internal/core"
)

// portsTool adapts the ports controller: /api/ports (list) and /api/ports/*
// (label/protected/kill), plus the /ports page.
type portsTool struct{ ctl *portsctl.Controller }

func newPorts(ctl *portsctl.Controller) core.Tool { return portsTool{ctl: ctl} }

func (t portsTool) Meta() core.Meta {
	return core.Meta{
		ID:    "ports",
		Title: "ports",
		Icon:  "⌁",
		Desc:  "開いている TCP ポートの確認、ラベル付け、保護対象設定、LISTEN プロセスの kill",
		Page:  "tools/ports/index.html",
	}
}

func (t portsTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/ports", Handle: t.ctl.HandleGet},
		{Method: http.MethodPost, Pattern: "/api/ports/", Prefix: true, Handle: func(w http.ResponseWriter, r *http.Request) error {
			return t.ctl.HandlePost(w, r, decodeBody(w, r))
		}},
	}
}
