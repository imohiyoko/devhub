package containers

// The endpoints that act on one container instead of listing it.
//
// All POSTs, which is what puts them behind /ai-api's manual-approval gate for
// free (aiAPINeedsApproval). That matters more here than for the profile
// endpoints: this panel deliberately shows containers no environment declared,
// so an agent reading it can name anything on the machine. It can ask; it
// cannot have it without the user saying yes.
//
// There is no dry run, unlike a resize, and the difference is the blast radius.
// A resize takes down every container in a VM, including ones belonging to
// environments that are nowhere on screen, so the user has to be told what they
// are agreeing to. Stopping one container stops the one container named in the
// request — the row the user clicked — and starting it again brings it back.
// The response says which container it was, so a caller that got the wrong one
// finds out from the answer.
//
// start is here for that last reason and not as an afterthought: a panel that
// can only take things down leaves the user in a terminal to undo it, which is
// the state this tool exists to end.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/imohiyoko/devhub/internal/container"
	"github.com/imohiyoko/devhub/internal/httpx"
)

// operator is the narrow view of the container package this file needs,
// declared in the consumer so the handlers are testable without a daemon.
type operator interface {
	Resolve(ctx context.Context, sourceID, id string) (container.ContainerTarget, error)
	Logs(ctx context.Context, src container.Source, id string, tail int) (string, error)
	Stop(ctx context.Context, src container.Source, id string) error
	Start(ctx context.Context, src container.Source, id string) error
	Restart(ctx context.Context, src container.Source, id string) error
}

// controlVerbs is the closed set of subcommands this handler will run. A map
// rather than a chain of comparisons because the set is now long enough that a
// missing negation would read as correct — and this is the one handler where an
// unintended verb would apply to anything on the machine, not to a declared
// project.
var controlVerbs = map[string]bool{"logs": true, "stop": true, "start": true, "restart": true}

// HandleControlPost serves /api/containers/{logs,stop,start,restart}. One
// handler because they differ only in the verb: each names a source and a
// container, and each is refused in the same place for the same reasons.
func (c *Controller) HandleControlPost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	verb := strings.TrimPrefix(r.URL.Path, "/api/containers/")
	if !controlVerbs[verb] {
		return httpx.Errorf(http.StatusNotFound, "not found")
	}

	sourceID, _ := data["source"].(string)
	id, _ := data["id"].(string)
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(id) == "" {
		return httpx.Errorf(http.StatusBadRequest, "source と id が必要です")
	}

	// Resolved before anything spawns. This is the bound the whole Surface
	// rests on: the ID that reaches a command line is one the engine itself
	// just reported, on a source devhub knows.
	target, err := c.control.Resolve(r.Context(), sourceID, id)
	if err != nil {
		return controlError(err)
	}

	switch verb {
	case "logs":
		// A read, so it stays on the request context: nobody is waiting for
		// logs whose reader has gone.
		out, err := c.control.Logs(r.Context(), target.Source, target.Container.ID, pTail(data))
		if err != nil {
			return controlError(err)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"ok": true, "container": targetJSON(target), "logs": out,
		})
		return nil
	case "stop":
		err = c.control.Stop(acting(r), target.Source, target.Container.ID)
	case "start":
		err = c.control.Start(acting(r), target.Source, target.Container.ID)
	case "restart":
		err = c.control.Restart(acting(r), target.Source, target.Container.ID)
	}
	if err != nil {
		return controlError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "action": verb, "container": targetJSON(target),
	})
	return nil
}

// targetJSON says which container was acted on. It is echoed back rather than
// assumed, because the request named an ID and the user was looking at a name:
// a caller that addressed the wrong row learns it from the answer.
func targetJSON(t container.ContainerTarget) map[string]any {
	return map[string]any{
		"id":      t.Container.ID,
		"name":    t.Container.DisplayName(),
		"project": t.Container.Project,
		"source":  t.Source.ID,
		"label":   t.Source.Label,
	}
}

// controlError maps the container package's refusals onto status codes. As in
// profiles.go, anything else gets an explicit 500 rather than httpx's default
// 400, so a caller can tell "that container is gone" from "devhub tried and
// something broke".
func controlError(err error) error {
	switch {
	case errors.Is(err, container.ErrContainerMissing):
		return httpx.Errorf(http.StatusNotFound, "%s", err.Error())
	case errors.Is(err, container.ErrSourceMissing):
		return httpx.Errorf(http.StatusNotFound, "%s", err.Error())
	}
	return httpx.Errorf(http.StatusInternalServerError, "%s", err.Error())
}

// pTail reads the requested history length. Absent is zero, which the container
// package turns into its default; anything unreasonable is clamped there too,
// so this only has to refuse what is not a number at all.
func pTail(data map[string]any) int {
	switch v := data["tail"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
