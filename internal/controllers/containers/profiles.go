package containers

// The two endpoints that act on a Colima VM instead of reading one.
//
// Both are POSTs, which is what puts them behind /ai-api's manual-approval gate
// for free (aiAPINeedsApproval): an agent can ask devhub for a VM, it cannot
// have one without the user saying yes. Nothing else about them is special-cased
// for that path — the browser and an agent go through the same handler.
//
// Resize follows the shape env-launcher already uses for switches: ask first,
// act second. A call without confirm is a dry run that answers "what would this
// stop", and the answer is the point — a resize takes down every container in
// the VM, including ones belonging to environments that merely share the
// profile and are nowhere on the screen the user is looking at.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/imohiyoko/devhub/internal/container"
	"github.com/imohiyoko/devhub/internal/httpx"
)

// admin is the narrow view of the container package's VM operations, declared
// in the consumer so these handlers are testable without a machine that boots
// virtual machines in response.
type admin interface {
	Create(ctx context.Context, spec container.ProfileSpec) error
	Resize(ctx context.Context, spec container.ProfileSpec) error
	CheckResize(ctx context.Context, spec container.ProfileSpec) error
	ProfileTargets(ctx context.Context, name string) ([]container.Container, error)
}

// HandleProfilePost serves both /api/containers/profiles and
// /api/containers/profiles/{name}/resize. One handler because the routes differ
// only in which of two verbs they end in, and the request bodies are the same
// shape.
func (c *Controller) HandleProfilePost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	const prefix = "/api/containers/profiles"
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")

	switch {
	case rest == "":
		return c.createProfile(w, r, data)
	case strings.HasSuffix(rest, "/resize"):
		return c.resizeProfile(w, r, strings.TrimSuffix(rest, "/resize"), data)
	default:
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
}

// createProfile brings up a profile that does not exist yet. It is the safe
// half: there is no VM, so there is nothing to take down, which is why it needs
// no confirmation step.
func (c *Controller) createProfile(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	spec, err := decodeSpec(data, pStr(data, "name"))
	if err != nil {
		return err
	}
	if err := c.admin.Create(r.Context(), spec); err != nil {
		return profileError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "profile": spec.Name, "spec": describeSpecJSON(spec),
	})
	return nil
}

// resizeProfile applies a new size to an existing profile, or — without an
// explicit confirm — reports what doing so would stop.
//
// The dry run is not a formality. Colima reads sizes only at start, so a resize
// is a stop and a start, and the containers it takes down may belong to an
// environment the user is not thinking about: two definitions can name the same
// profile, and nothing in either one says so.
func (c *Controller) resizeProfile(w http.ResponseWriter, r *http.Request, name string, data map[string]any) error {
	spec, err := decodeSpec(data, name)
	if err != nil {
		return err
	}

	// Refuse before asking. A user who has just agreed to stop a VM full of
	// containers should not then be told the resize was never possible.
	if err := c.admin.CheckResize(r.Context(), spec); err != nil {
		return profileError(err)
	}
	targets, err := c.admin.ProfileTargets(r.Context(), spec.Name)
	if err != nil {
		return profileError(err)
	}
	stops := make([]any, 0, len(targets))
	for _, t := range targets {
		stops = append(stops, map[string]any{
			"id": t.ID, "name": t.DisplayName(), "project": t.Project, "running": t.Running(),
		})
	}

	if b, _ := data["confirm"].(bool); !b {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"ok": false, "confirm_required": true,
			"profile": spec.Name, "spec": describeSpecJSON(spec), "stops": stops,
		})
		return nil
	}

	if err := c.admin.Resize(r.Context(), spec); err != nil {
		return profileError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "profile": spec.Name, "spec": describeSpecJSON(spec), "stopped": stops,
	})
	return nil
}

// decodeSpec reads the request body. Sizes are optional and absent means "leave
// colima's default" — which the container package turns into an omitted flag,
// not a zero.
func decodeSpec(data map[string]any, name string) (container.ProfileSpec, error) {
	spec := container.ProfileSpec{
		Name:   strings.TrimSpace(name),
		Engine: strings.TrimSpace(pStr(data, "engine")),
	}
	for _, f := range []struct {
		key string
		dst *int
	}{
		{"cpus", &spec.CPUs}, {"memory_gib", &spec.MemoryGiB}, {"disk_gib", &spec.DiskGiB},
	} {
		v, err := pSize(data, f.key)
		if err != nil {
			return spec, err
		}
		*f.dst = v
	}
	// Checked here as well as in the container package: the name reaches this
	// layer from a URL path, and rejecting it before the request goes any
	// further keeps a bad one out of logs and error messages too.
	if !container.ValidProfileName(spec.Name) {
		return spec, httpx.Errorf(http.StatusBadRequest,
			"profile 名は英数字と _ - のみ、先頭は英数字か _ です")
	}
	return spec, nil
}

// profileError maps the container package's refusals onto status codes. They
// are refusals rather than failures — nothing was attempted — so they must not
// read as 500s, and the message is already written for the person who will see
// it.
func profileError(err error) error {
	switch {
	case errors.Is(err, container.ErrProfileExists):
		return httpx.Errorf(http.StatusConflict, "%s", err.Error())
	case errors.Is(err, container.ErrProfileMissing):
		return httpx.Errorf(http.StatusNotFound, "%s", err.Error())
	case errors.Is(err, container.ErrDiskShrink),
		errors.Is(err, container.ErrColimaUnsupportedOS),
		errors.Is(err, container.ErrColimaMissing):
		return httpx.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return err
}

func describeSpecJSON(spec container.ProfileSpec) map[string]any {
	out := map[string]any{}
	if spec.CPUs > 0 {
		out["cpus"] = spec.CPUs
	}
	if spec.MemoryGiB > 0 {
		out["memory_gib"] = spec.MemoryGiB
	}
	if spec.DiskGiB > 0 {
		out["disk_gib"] = spec.DiskGiB
	}
	if spec.Engine != "" {
		out["engine"] = spec.Engine
	}
	return out
}

func pStr(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

// pSize reads one size. Absent is zero, which means "leave colima's default".
//
// The rejections happen here rather than being left to the container package.
// JSON numbers decode as float64, and turning a request for 1.5 CPUs into 1 is
// not an answer to what was asked; passing a sentinel on and relying on a check
// in another package to catch it would work only for as long as that check
// happens to reject the same values.
func pSize(data map[string]any, key string) (int, error) {
	raw, present := data[key]
	if !present || raw == nil {
		return 0, nil
	}
	var f float64
	switch v := raw.(type) {
	case float64:
		f = v
	case int:
		f = float64(v)
	default:
		return 0, httpx.Errorf(http.StatusBadRequest, "%s は数値で指定してください", key)
	}
	if f != float64(int(f)) {
		return 0, httpx.Errorf(http.StatusBadRequest, "%s は整数で指定してください（%v）", key, f)
	}
	if f < 0 {
		return 0, httpx.Errorf(http.StatusBadRequest, "%s は 0 以上で指定してください", key)
	}
	return int(f), nil
}
