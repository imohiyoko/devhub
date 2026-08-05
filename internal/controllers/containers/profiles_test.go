package containers

// Tests for the two endpoints that move a VM. The theme is the same as in
// internal/container: most of what is asserted is that nothing happened.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imohiyoko/devhub/internal/container"
	"github.com/imohiyoko/devhub/internal/httpx"
)

type fakeAdmin struct {
	created []container.ProfileSpec
	resized []container.ProfileSpec
	checked []container.ProfileSpec
	started []string
	stopped []string
	targets []container.Container
	budget  container.Budget
	err     error
	// actCtxErr is the state of the context the operation was handed. A
	// cancelled one means it would be killed mid-flight.
	actCtxErr error
}

// moved reports how many times this fake was asked to touch a VM. Tests that
// assert nothing happened use it rather than naming the fields, so a new
// operation added to the admin cannot slip past them by being unlisted.
func (f *fakeAdmin) moved() int {
	return len(f.created) + len(f.resized) + len(f.started) + len(f.stopped)
}

func (f *fakeAdmin) Create(ctx context.Context, spec container.ProfileSpec) error {
	f.actCtxErr = ctx.Err()
	f.created = append(f.created, spec)
	return f.err
}

func (f *fakeAdmin) Resize(ctx context.Context, spec container.ProfileSpec) error {
	f.actCtxErr = ctx.Err()
	f.resized = append(f.resized, spec)
	return f.err
}

func (f *fakeAdmin) CheckResize(_ context.Context, spec container.ProfileSpec) error {
	f.checked = append(f.checked, spec)
	return f.err
}

func (f *fakeAdmin) Start(ctx context.Context, name string) error {
	f.actCtxErr = ctx.Err()
	f.started = append(f.started, name)
	return f.err
}

func (f *fakeAdmin) Stop(ctx context.Context, name string) error {
	f.actCtxErr = ctx.Err()
	f.stopped = append(f.stopped, name)
	return f.err
}

func (f *fakeAdmin) ProfileTargets(context.Context, string) ([]container.Container, error) {
	return f.targets, f.err
}

// Budget deliberately ignores f.err: it is a read of host facts the caller
// shows, not one of the operations a test is asserting refusals for.
func (f *fakeAdmin) Budget(context.Context) (container.Budget, error) {
	return f.budget, nil
}

func post(t *testing.T, a *fakeAdmin, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	c := &Controller{admin: a}
	if err := c.HandleProfilePost(rr, httptest.NewRequest(http.MethodPost, path, nil), body); err != nil {
		// Rendered by the same function the server uses, rather than restating
		// its mapping here: a helper that picks its own statuses can agree with
		// the test and disagree with production, and then the status assertions
		// below are only checking the helper.
		httpx.WriteError(rr, err)
	}
	var out map[string]any
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatalf("decode: %v (body %s)", e, rr.Body)
	}
	return rr.Code, out
}

func TestCreateProfilePassesTheSpecThrough(t *testing.T) {
	a := &fakeAdmin{}
	code, out := post(t, a, "/api/containers/profiles",
		map[string]any{"name": "big", "cpus": float64(8), "memory_gib": float64(16), "engine": "containerd"})

	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if len(a.created) != 1 {
		t.Fatalf("created = %v", a.created)
	}
	got := a.created[0]
	want := container.ProfileSpec{Name: "big", CPUs: 8, MemoryGiB: 16, Engine: "containerd"}
	if got != want {
		t.Errorf("spec = %+v, want %+v", got, want)
	}
	// An omitted size stays zero, which the container package turns into an
	// omitted flag rather than a literal 0.
	if got.DiskGiB != 0 {
		t.Errorf("disk = %d, want 0 (absent)", got.DiskGiB)
	}
}

// TestBadRequestsNeverReachTheAdmin: the name arrives from a URL path or a
// request body, and both are checked before anything is handed to a command
// line.
func TestBadRequestsNeverReachTheAdmin(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"empty name", "/api/containers/profiles", map[string]any{"cpus": float64(2)}},
		{"flag-like name", "/api/containers/profiles", map[string]any{"name": "--rm"}},
		{"space in name", "/api/containers/profiles", map[string]any{"name": "my profile"}},
		{"path traversal in the URL", "/api/containers/profiles/..%2f..%2fetc/resize", map[string]any{"confirm": true}},
		// The route is a prefix, so this arrives here too — and without a
		// separator check it reads as a resize of "dev".
		{"no separator after the prefix", "/api/containers/profilesdev/resize", map[string]any{"confirm": true}},
		// The name is one segment. Cutting at the last separator instead would
		// make this a stop of "a".
		{"extra segments before the verb", "/api/containers/profiles/a/b/stop", map[string]any{"confirm": true}},
		{"flag-like name in the URL", "/api/containers/profiles/--rm/stop", map[string]any{"confirm": true}},
		{"missing name before the verb", "/api/containers/profiles/start", map[string]any{}},
		// A fractional CPU count is refused rather than truncated: turning a
		// request for 1.5 into 1 answers a question nobody asked.
		{"fractional cpus", "/api/containers/profiles", map[string]any{"name": "ok", "cpus": 1.5}},
	} {
		a := &fakeAdmin{}
		code, out := post(t, a, tc.path, tc.body)
		if code == http.StatusOK {
			t.Errorf("%s: accepted (%v)", tc.name, out)
		}
		if a.moved() > 0 {
			t.Errorf("%s: reached the admin: created=%v resized=%v started=%v stopped=%v",
				tc.name, a.created, a.resized, a.started, a.stopped)
		}
	}
}

// TestResizeWithoutConfirmOnlyReports is the safety shape: a resize takes every
// container in the VM down, so the first call answers "what would this stop"
// and changes nothing.
func TestResizeWithoutConfirmOnlyReports(t *testing.T) {
	a := &fakeAdmin{targets: []container.Container{
		{ID: "a1", Name: "other-envs-db", State: "running", Project: "someone-else"},
		{ID: "b2", Name: "", State: "running"},
	}}
	code, out := post(t, a, "/api/containers/profiles/shared/resize", map[string]any{"cpus": float64(8)})

	if code != http.StatusOK {
		t.Fatalf("code = %d (%v)", code, out)
	}
	if out["ok"] != false || out["confirm_required"] != true {
		t.Errorf("out = %v, want a confirmation request", out)
	}
	if len(a.resized) != 0 {
		t.Fatalf("resized without confirmation: %v", a.resized)
	}
	// The refusals are evaluated before the user is asked to agree to anything.
	if len(a.checked) != 1 {
		t.Errorf("the dry run did not validate the spec: %v", a.checked)
	}

	stops, _ := out["stops"].([]any)
	if len(stops) != 2 {
		t.Fatalf("stops = %v, want both containers named", out["stops"])
	}
	first, _ := stops[0].(map[string]any)
	if first["project"] != "someone-else" {
		t.Errorf("the owning project was not reported: %v", first)
	}
	// A nameless container still has to be identifiable in the confirmation.
	second, _ := stops[1].(map[string]any)
	if second["name"] != "b2" {
		t.Errorf("nameless container rendered as %v, want its ID", second["name"])
	}
}

func TestResizeWithConfirmActs(t *testing.T) {
	a := &fakeAdmin{targets: []container.Container{{ID: "a1", Name: "db", State: "running"}}}
	code, out := post(t, a, "/api/containers/profiles/dev/resize",
		map[string]any{"cpus": float64(4), "confirm": true})

	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if len(a.resized) != 1 || a.resized[0].Name != "dev" || a.resized[0].CPUs != 4 {
		t.Fatalf("resized = %+v", a.resized)
	}
	// What was taken down is reported back, so the result is not just "ok".
	if stopped, _ := out["stopped"].([]any); len(stopped) != 1 {
		t.Errorf("stopped = %v, want the container that went down", out["stopped"])
	}
}

// TestRefusalsGetTheirOwnStatus: these are refusals, not failures — nothing was
// attempted — so a caller (and an agent reading the response) can tell them
// apart from a devhub bug.
func TestRefusalsGetTheirOwnStatus(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{container.ErrProfileExists, http.StatusConflict},
		{container.ErrProfileMissing, http.StatusNotFound},
		{container.ErrDiskShrink, http.StatusBadRequest},
		{container.ErrEngineChange, http.StatusBadRequest},
		{container.ErrColimaMissing, http.StatusBadRequest},
		{container.ErrColimaUnsupportedOS, http.StatusBadRequest},
	} {
		a := &fakeAdmin{err: tc.err}
		code, out := post(t, a, "/api/containers/profiles", map[string]any{"name": "x"})
		if code != tc.want {
			t.Errorf("%v: code = %d, want %d", tc.err, code, tc.want)
		}
		// The container package's wording is what the user sees; the controller
		// does not paraphrase it.
		if msg, _ := out["error"].(string); !strings.Contains(msg, tc.err.Error()) {
			t.Errorf("%v: message = %q, want the original wording", tc.err, msg)
		}
	}

	// Anything else is a real failure and must not be dressed up as a refusal.
	// httpx.WriteError would otherwise render a bare error as 400, which is the
	// same status a rejected request gets — so the caller could not tell "your
	// spec was wrong, nothing happened" from "the stop ran and the start did
	// not". profileError gives those a 500 on purpose.
	a := &fakeAdmin{err: errors.New("boom")}
	if code, _ := post(t, a, "/api/containers/profiles", map[string]any{"name": "x"}); code != http.StatusInternalServerError {
		t.Errorf("an unknown failure got %d, want 500 — indistinguishable from a refusal", code)
	}
}

// TestOperationsOutliveTheRequest. exec.CommandContext kills the process when
// its context ends, and a request context ends the moment the browser tab
// closes. For a listing that is correct — nobody is waiting for it. For a
// resize it is not: the stop has already run by then, so the VM is left down
// with neither the old size nor the new one.
func TestOperationsOutliveTheRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"create", "/api/containers/profiles", map[string]any{"name": "x"}},
		{"resize", "/api/containers/profiles/dev/resize", map[string]any{"cpus": float64(2), "confirm": true}},
		{"start", "/api/containers/profiles/dev/start", map[string]any{}},
		{"stop", "/api/containers/profiles/dev/stop", map[string]any{"confirm": true}},
	} {
		a := &fakeAdmin{}
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPost, tc.path, nil).WithContext(ctx)
		cancel() // the tab is gone before the handler gets anywhere

		c := &Controller{admin: a}
		if err := c.HandleProfilePost(httptest.NewRecorder(), req, tc.body); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if a.actCtxErr != nil {
			t.Errorf("%s: ran under a cancelled context (%v); colima would be killed", tc.name, a.actCtxErr)
		}
	}
}

// TestUnknownProfileRouteIs404: the verb set is closed. Nothing here removes a
// VM, and a subcommand devhub never meant to offer cannot be reached by putting
// it in the path.
func TestUnknownProfileRouteIs404(t *testing.T) {
	for _, path := range []string{
		"/api/containers/profiles/dev/destroy",
		"/api/containers/profiles/dev/delete",
		"/api/containers/profiles/dev/restart",
	} {
		a := &fakeAdmin{}
		if code, _ := post(t, a, path, map[string]any{"confirm": true}); code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404 — only create, resize, start and stop exist", path, code)
		}
		if a.moved() > 0 {
			t.Errorf("%s: an unknown verb reached the admin", path)
		}
	}
}

// TestStopWithoutConfirmOnlyReports: a stop takes down every container in the
// VM, exactly as a resize does, so it gets the same dry run. The first call
// answers "what would this stop" and changes nothing.
func TestStopWithoutConfirmOnlyReports(t *testing.T) {
	a := &fakeAdmin{targets: []container.Container{
		{ID: "a1", Name: "other-envs-db", State: "running", Project: "someone-else"},
		{ID: "b2", Name: "", State: "running"},
	}}
	code, out := post(t, a, "/api/containers/profiles/shared/stop", map[string]any{})

	if code != http.StatusOK {
		t.Fatalf("code = %d (%v)", code, out)
	}
	if out["ok"] != false || out["confirm_required"] != true {
		t.Errorf("out = %v, want a confirmation request", out)
	}
	if len(a.stopped) != 0 {
		t.Fatalf("stopped without confirmation: %v", a.stopped)
	}
	stops, _ := out["stops"].([]any)
	if len(stops) != 2 {
		t.Fatalf("stops = %v, want both containers named", out["stops"])
	}
	// The owning project is the point of the list: a container belonging to an
	// environment that merely shares the profile is the one the user cannot
	// work out from the screen.
	first, _ := stops[0].(map[string]any)
	if first["project"] != "someone-else" {
		t.Errorf("the owning project was not reported: %v", first)
	}
	second, _ := stops[1].(map[string]any)
	if second["name"] != "b2" {
		t.Errorf("nameless container rendered as %v, want its ID", second["name"])
	}
}

func TestStopWithConfirmActs(t *testing.T) {
	a := &fakeAdmin{targets: []container.Container{{ID: "a1", Name: "db", State: "running"}}}
	code, out := post(t, a, "/api/containers/profiles/dev/stop", map[string]any{"confirm": true})

	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if len(a.stopped) != 1 || a.stopped[0] != "dev" {
		t.Fatalf("stopped = %v", a.stopped)
	}
	if stopped, _ := out["stopped"].([]any); len(stopped) != 1 {
		t.Errorf("stopped = %v, want the container that went down", out["stopped"])
	}
}

// TestStartActsWithoutConfirmation: a start takes nothing down, so asking would
// be a confirmation with nothing to confirm — and it is what makes stop
// offerable at all, so it must not be the harder of the two.
func TestStartActsWithoutConfirmation(t *testing.T) {
	a := &fakeAdmin{}
	code, out := post(t, a, "/api/containers/profiles/dev/start", map[string]any{})

	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("code=%d out=%v", code, out)
	}
	if len(a.started) != 1 || a.started[0] != "dev" {
		t.Fatalf("started = %v", a.started)
	}
	// A start does not resize, so it must not have gone looking for a size or
	// asked what a restart would take down.
	if len(a.resized) > 0 || len(a.checked) > 0 {
		t.Errorf("a start touched the resize path: resized=%v checked=%v", a.resized, a.checked)
	}
}

// TestUnknownProfileIsNotFound: Start refuses a name that does not exist rather
// than creating it — Create is the door for a VM that does not exist, and a typo
// must not become a default-sized machine.
func TestUnknownProfileIsNotFound(t *testing.T) {
	for _, path := range []string{
		"/api/containers/profiles/nope/start",
		"/api/containers/profiles/nope/stop",
	} {
		a := &fakeAdmin{err: container.ErrProfileMissing}
		if code, _ := post(t, a, path, map[string]any{"confirm": true}); code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404", path, code)
		}
	}
}

// TestResizeRefusesBeforeAsking: being told "the disk cannot shrink" after
// consenting to stop a VM full of containers is a worse sequence than being
// told first, and the consent would have been given for an operation that
// could never run.
func TestResizeRefusesBeforeAsking(t *testing.T) {
	a := &fakeAdmin{
		err:     container.ErrDiskShrink,
		targets: []container.Container{{ID: "a1", Name: "db", State: "running"}},
	}
	code, out := post(t, a, "/api/containers/profiles/dev/resize", map[string]any{"disk_gib": float64(5)})

	if code != http.StatusBadRequest {
		t.Fatalf("code = %d (%v), want the refusal on the dry run", code, out)
	}
	if out["confirm_required"] == true {
		t.Error("asked for confirmation of a resize that cannot happen")
	}
	if len(a.resized) != 0 {
		t.Error("resized despite the refusal")
	}
}

// TestOverCapacityIsARefusal, not a 500: nothing was attempted and the machine
// is untouched, which is a thing a caller (and an agent reading the response)
// acts on differently from "devhub tried and something broke".
func TestOverCapacityIsARefusal(t *testing.T) {
	a := &fakeAdmin{err: container.ErrOverHostCapacity}
	code, out := post(t, a, "/api/containers/profiles", map[string]any{"name": "big", "cpus": float64(64)})
	if code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, container.ErrOverHostCapacity.Error()) {
		t.Errorf("message = %q, want the container package's wording", msg)
	}
}

// TestStartWarnsWhenTheVMsTogetherOversubscribe. A warning, not a refusal: the
// cap bounds one VM, and two being up at once is something the user may well
// have meant. What they cannot do is notice it — nothing on the panel adds the
// profiles up — so the answer says it once.
func TestStartWarnsWhenTheVMsTogetherOversubscribe(t *testing.T) {
	a := &fakeAdmin{budget: container.Budget{
		Detected: true, HostMemBytes: 32 << 30, MemCapGiB: 25, RunningMemGiB: 36,
	}}
	code, out := post(t, a, "/api/containers/profiles/dev/start", map[string]any{})

	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("the start was refused: code=%d out=%v", code, out)
	}
	if len(a.started) != 1 {
		t.Fatalf("started = %v — the warning must not have blocked it", a.started)
	}
	warning, _ := out["warning"].(string)
	if warning == "" {
		t.Fatal("no warning for 36 GiB allocated against a 25 GiB cap")
	}
	for _, want := range []string{"36 GiB", "25 GiB", "32 GiB"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning is missing %q:\n%s", want, warning)
		}
	}
}

// TestStartIsQuietWhenItFits — and when devhub cannot measure the host at all,
// since a warning computed from zeros would be about a machine nobody has.
func TestStartIsQuietWhenItFits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget container.Budget
	}{
		{"within the cap", container.Budget{Detected: true, MemCapGiB: 25, RunningMemGiB: 20}},
		{"exactly at the cap", container.Budget{Detected: true, MemCapGiB: 25, RunningMemGiB: 25}},
		{"host unknown", container.Budget{Detected: false, MemCapGiB: 0, RunningMemGiB: 99}},
	} {
		a := &fakeAdmin{budget: tc.budget}
		_, out := post(t, a, "/api/containers/profiles/dev/start", map[string]any{})
		if w, _ := out["warning"].(string); w != "" {
			t.Errorf("%s: warned anyway:\n%s", tc.name, w)
		}
	}
}
