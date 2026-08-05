package containers

// The endpoints that act on a Colima VM instead of reading one.
//
// All POSTs, which is what puts them behind /ai-api's manual-approval gate for
// free (aiAPINeedsApproval): an agent can ask devhub for a VM, it cannot have
// one without the user saying yes. Nothing else about them is special-cased for
// that path — the browser and an agent go through the same handler.
//
// They split by blast radius, not by verb:
//
//   - create and start bring a VM up. Nothing is running that they could take
//     down, so they act on the first call.
//   - resize and stop take one down, and with it every container in the VM.
//     Both follow the shape env-launcher already uses for switches: ask first,
//     act second. A call without confirm is a dry run that answers "what would
//     this stop", and the answer is the point — the containers that go down may
//     belong to environments that merely share the profile and are nowhere on
//     the screen the user is looking at.
//
// start is what makes stop offerable. Without it the panel could take a VM down
// and not bring it back, which would leave the user in a terminal for the
// second half of an operation devhub started.

import (
	"context"
	"errors"
	"fmt"
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
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	ProfileTargets(ctx context.Context, name string) ([]container.Container, error)
	Budget(ctx context.Context) (container.Budget, error)
}

// HandleProfilePost serves /api/containers/profiles and
// /api/containers/profiles/{name}/{resize,start,stop}. One handler because the
// routes differ only in the verb they end in, and the path is parsed the same
// way for all of them.
func (c *Controller) HandleProfilePost(w http.ResponseWriter, r *http.Request, data map[string]any) error {
	const prefix = "/api/containers/profiles"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	// The route is registered as a prefix, so /api/containers/profilesdev also
	// lands here and would otherwise resize "dev". Only a real separator
	// continues this route; anything else is a different URL that happens to
	// begin with the same letters.
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	rest = strings.Trim(rest, "/")

	if rest == "" {
		return c.createProfile(w, r, data)
	}
	name, verb, ok := strings.Cut(rest, "/")
	// Cut on the first separator, not the last: the name is the segment devhub
	// validates, and letting a path with extra segments fall through to a
	// suffix match would mean /api/containers/profiles/a/b/stop stopping "a".
	if !ok {
		return httpx.Errorf(http.StatusNotFound, "not found")
	}
	switch verb {
	case "resize":
		return c.resizeProfile(w, r, name, data)
	case "start":
		return c.startProfile(w, r, name)
	case "stop":
		return c.stopProfile(w, r, name, data)
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
	if err := c.admin.Create(acting(r), spec); err != nil {
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
	stops := stopListJSON(targets)

	if b, _ := data["confirm"].(bool); !b {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"ok": false, "confirm_required": true,
			"profile": spec.Name, "spec": describeSpecJSON(spec), "stops": stops,
		})
		return nil
	}

	if err := c.admin.Resize(acting(r), spec); err != nil {
		return profileError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "profile": spec.Name, "spec": describeSpecJSON(spec), "stopped": stops,
	})
	return nil
}

// startProfile brings an existing profile back up. It is the counterpart to
// createProfile in the one way that decides the shape of this handler: nothing
// is running that a start could take down, so there is nothing to warn about
// and no confirmation step.
//
// It carries no body at all. A start is not a place to change the VM's size —
// colima keeps each profile's configuration and this passes no flags — and
// accepting sizes here would make it a resize wearing another name, without the
// dry run a resize is required to have.
func (c *Controller) startProfile(w http.ResponseWriter, r *http.Request, name string) error {
	name, err := profileName(name)
	if err != nil {
		return err
	}
	// Read before the start, so the totals describe the machine the user is
	// about to add to rather than the one they just changed.
	budget, _ := c.admin.Budget(r.Context())

	if err := c.admin.Start(acting(r), name); err != nil {
		return profileError(err)
	}
	out := map[string]any{"ok": true, "action": "start", "profile": name}
	if w := oversubscribed(budget); w != "" {
		out["warning"] = w
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// oversubscribed says when the VMs that are up have between them been promised
// more memory than devhub would give any single one.
//
// A warning and not a refusal, deliberately. The cap bounds one VM against what
// the machine has; this is a different claim — that several VMs together are
// over the line — and it is one the user may well have meant, because the two
// are rarely busy at the same moment. What they cannot do is notice it: nothing
// on the screen adds the profiles up. So devhub says it once and gets out of
// the way.
//
// Measured against the cap rather than physical memory, which is what makes it
// arrive early enough to act on: by the time the total passes what the Mac
// actually has, the swapping has already started.
func oversubscribed(b container.Budget) string {
	if !b.Detected || b.MemCapGiB <= 0 || b.RunningMemGiB <= b.MemCapGiB {
		return ""
	}
	return fmt.Sprintf(
		"起動中の VM に割り当てられたメモリの合計が %d GiB になり、1 VM あたりの上限 %d GiB を超えています（実装 %d GiB）。同時に使う場合は、どれかを停止するかサイズを見直してください。",
		b.RunningMemGiB, b.MemCapGiB, b.HostMemBytes/(1<<30))
}

// stopProfile shuts a profile down, or — without an explicit confirm — reports
// what doing so would take with it.
//
// The dry run is the same one resize has, for the same reason: the blast radius
// is identical. Every container in the VM goes down, and which ones those are is
// not something the user can work out from the screen, because two environments
// can name the same profile and neither definition says so.
//
// Unlike resize there is nothing to refuse ahead of time. A stop cannot be
// impossible the way a disk shrink is, and it is undone by starting the profile
// again — so the only thing that fails here is a profile that does not exist,
// which ProfileTargets answers on the dry run before anyone agrees to anything.
func (c *Controller) stopProfile(w http.ResponseWriter, r *http.Request, name string, data map[string]any) error {
	name, err := profileName(name)
	if err != nil {
		return err
	}
	targets, err := c.admin.ProfileTargets(r.Context(), name)
	if err != nil {
		return profileError(err)
	}
	stops := stopListJSON(targets)

	if b, _ := data["confirm"].(bool); !b {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"ok": false, "confirm_required": true, "action": "stop",
			"profile": name, "stops": stops,
		})
		return nil
	}

	if err := c.admin.Stop(acting(r), name); err != nil {
		return profileError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "action": "stop", "profile": name, "stopped": stops,
	})
	return nil
}

// stopListJSON names the containers an operation would take down. The project
// is the part that matters: a container from a project the user was not
// thinking about is exactly the one they need to see before saying yes.
func stopListJSON(targets []container.Container) []any {
	stops := make([]any, 0, len(targets))
	for _, t := range targets {
		stops = append(stops, map[string]any{
			"id": t.ID, "name": t.DisplayName(), "project": t.Project, "running": t.Running(),
		})
	}
	return stops
}

// profileName checks a name that arrived in a URL path. The same rule
// decodeSpec applies, factored out for the two endpoints that take no spec —
// so a name reaching colima is checked once, by one rule, whichever route
// carried it.
func profileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !container.ValidProfileName(name) {
		return "", httpx.Errorf(http.StatusBadRequest,
			"profile 名は英数字と _ - のみ、先頭は英数字か _ です")
	}
	return name, nil
}

// acting is the context a VM operation runs under. It deliberately outlives the
// request.
//
// exec.CommandContext kills the process when its context ends, and a request
// context ends the moment the browser tab closes. For a listing that is the
// right answer — nobody is waiting for it. For a resize it is not: by then the
// stop has already run, so killing the start leaves the VM down with neither
// the old size nor the new one, which is exactly the state the dry run exists
// to keep the user out of. envs made the same call for compose up and stop
// (apply.go), and for the same reason.
//
// This drops the cancellation, not the deadline: the container package puts
// profileOpTimeout on the operation, so nothing here can run unbounded.
func acting(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

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
	if _, err := profileName(spec.Name); err != nil {
		return spec, err
	}
	return spec, nil
}

// profileError maps the container package's refusals onto status codes. They
// are refusals rather than failures — nothing was attempted — so they must not
// read as 500s, and the message is already written for the person who will see
// it.
//
// Anything else gets an explicit 500, which is a deliberate departure from
// httpx.WriteError's default of 400 for a bare error. Here the difference
// carries information a caller acts on: a refusal means the request was wrong
// and the machine is untouched, while a failure means devhub or colima tried
// and something is now in an unknown state — possibly a stopped VM. An agent
// that cannot tell those apart will retry the second one.
func profileError(err error) error {
	switch {
	case errors.Is(err, container.ErrProfileExists):
		return httpx.Errorf(http.StatusConflict, "%s", err.Error())
	case errors.Is(err, container.ErrProfileMissing):
		return httpx.Errorf(http.StatusNotFound, "%s", err.Error())
	case errors.Is(err, container.ErrDiskShrink),
		errors.Is(err, container.ErrEngineChange),
		errors.Is(err, container.ErrOverHostCapacity),
		errors.Is(err, container.ErrColimaUnsupportedOS),
		errors.Is(err, container.ErrColimaMissing):
		return httpx.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	return httpx.Errorf(http.StatusInternalServerError, "%s", err.Error())
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
