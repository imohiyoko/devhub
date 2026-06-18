package tools

import "github.com/imohiyoko/devhub/internal/core"

// pageTool is a frontend-only tool: it serves an embedded page and has no API.
// diff-kun and diagram are entirely client-side, so they need no controller.
type pageTool struct{ meta core.Meta }

func (t pageTool) Meta() core.Meta      { return t.meta }
func (t pageTool) Routes() []core.Route { return nil }

func newDiffKun() core.Tool {
	return pageTool{meta: core.Meta{
		ID:    "diff-kun",
		Title: "diff-kun",
		Icon:  "⇄",
		Desc:  "2つのテキストの差分をリアルタイム確認（ユニファイド / コンテキスト / サイドバイサイド）",
		Page:  "tools/diff-kun/index.html",
	}}
}

func newDiagram() core.Tool {
	return pageTool{meta: core.Meta{
		ID:    "diagram",
		Title: "diagram",
		Icon:  "◈",
		Desc:  "Mermaid 記法と Draw.io XML の相互変換。外部CDNは読み込まない",
		Page:  "tools/diagram/index.html",
	}}
}
