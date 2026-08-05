package tools

import (
	"net/http"
	"strings"

	"github.com/imohiyoko/devhub/internal/core"
	docspkg "github.com/imohiyoko/devhub/internal/docs"
	"github.com/imohiyoko/devhub/internal/httpx"
)

// docsTool serves devhub's embedded documentation over HTTP, so an agent that
// reached devhub through /ai-api can read its way out of a failure without
// dropping to a shell. It mirrors `devhub docs list` / `docs show` exactly —
// same Set, same names — because a caller told to read "agent/troubleshooting"
// must find it by that name whichever door it came through.
//
// API-only (no Page): documentation is not a dashboard card. Both routes are
// GETs, so /ai-api serves them without waiting for approval.
type docsTool struct{ set *docspkg.Set }

func newDocs(set *docspkg.Set) core.Tool { return docsTool{set: set} }

func (t docsTool) Meta() core.Meta {
	return core.Meta{ID: "docs", Title: "docs"} // Page empty → not a nav card
}

func (t docsTool) Routes() []core.Route {
	return []core.Route{
		{Method: http.MethodGet, Pattern: "/api/docs", Handle: t.list},
		{Method: http.MethodGet, Pattern: "/api/docs/", Prefix: true, Handle: t.show},
	}
}

func (t docsTool) list(w http.ResponseWriter, _ *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"docs": t.set.List(),
		"help": "GET /api/docs/<name> to read one of these.",
	})
	return nil
}

func (t docsTool) show(w http.ResponseWriter, r *http.Request) error {
	name := strings.TrimPrefix(r.URL.Path, "/api/docs/")
	if name == "" {
		return httpx.Errorf(http.StatusNotFound, "no document named").WithHint(
			"doc_not_found", "GET /api/docs lists the available names.")
	}
	body, err := t.set.Show(name)
	if err != nil {
		// Show's error already names the near misses; the hint adds the route to
		// call when none of them is right.
		return httpx.Errorf(http.StatusNotFound, "%s", err).WithHint(
			"doc_not_found", "GET /api/docs lists the available names.")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "body": body})
	return nil
}
