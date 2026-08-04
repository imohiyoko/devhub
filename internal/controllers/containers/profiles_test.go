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
	targets []container.Container
	err     error
	// actCtxErr is the state of the context Create or Resize was handed. A
	// cancelled one means the operation would be killed mid-flight.
	actCtxErr error
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

func (f *fakeAdmin) ProfileTargets(context.Context, string) ([]container.Container, error) {
	return f.targets, f.err
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
		// A fractional CPU count is refused rather than truncated: turning a
		// request for 1.5 into 1 answers a question nobody asked.
		{"fractional cpus", "/api/containers/profiles", map[string]any{"name": "ok", "cpus": 1.5}},
	} {
		a := &fakeAdmin{}
		code, out := post(t, a, tc.path, tc.body)
		if code == http.StatusOK {
			t.Errorf("%s: accepted (%v)", tc.name, out)
		}
		if len(a.created) > 0 || len(a.resized) > 0 {
			t.Errorf("%s: reached the admin: created=%v resized=%v", tc.name, a.created, a.resized)
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

func TestUnknownProfileRouteIs404(t *testing.T) {
	a := &fakeAdmin{}
	if code, _ := post(t, a, "/api/containers/profiles/dev/destroy", map[string]any{"confirm": true}); code != http.StatusNotFound {
		t.Errorf("code = %d, want 404 — only create and resize exist", code)
	}
	if len(a.created) > 0 || len(a.resized) > 0 {
		t.Error("an unknown verb reached the admin")
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
