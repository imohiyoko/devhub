package containers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/imohiyoko/devhub/internal/container"
	"github.com/imohiyoko/devhub/internal/httpx"
)

type fakeOperator struct {
	resolved  [][2]string
	stopped   []string
	started   []string
	restarted []string
	logged    []int
	target    container.ContainerTarget
	resolveEr error
	actErr    error
	actCtxErr error
}

func (f *fakeOperator) Resolve(_ context.Context, sourceID, id string) (container.ContainerTarget, error) {
	f.resolved = append(f.resolved, [2]string{sourceID, id})
	return f.target, f.resolveEr
}

func (f *fakeOperator) Logs(_ context.Context, _ container.Source, _ string, tail int) (string, error) {
	f.logged = append(f.logged, tail)
	return "line one\nline two", f.actErr
}

func (f *fakeOperator) Stop(ctx context.Context, _ container.Source, id string) error {
	f.actCtxErr = ctx.Err()
	f.stopped = append(f.stopped, id)
	return f.actErr
}

func (f *fakeOperator) Start(ctx context.Context, _ container.Source, id string) error {
	f.actCtxErr = ctx.Err()
	f.started = append(f.started, id)
	return f.actErr
}

func (f *fakeOperator) Restart(ctx context.Context, _ container.Source, id string) error {
	f.actCtxErr = ctx.Err()
	f.restarted = append(f.restarted, id)
	return f.actErr
}

func liveTarget() container.ContainerTarget {
	return container.ContainerTarget{
		Source:    container.Source{ID: "docker", Label: "Docker"},
		Container: container.Container{ID: "abc123def456", Name: "db", Project: "someone-else"},
	}
}

func ctlPost(t *testing.T, f *fakeOperator, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	c := &Controller{control: f}
	if err := c.HandleControlPost(rr, httptest.NewRequest(http.MethodPost, path, nil), body); err != nil {
		httpx.WriteError(rr, err)
	}
	var out map[string]any
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatalf("decode: %v (body %s)", e, rr.Body)
	}
	return rr.Code, out
}

func TestControlActsOnTheResolvedContainer(t *testing.T) {
	for _, tc := range []struct {
		verb string
		want func(*fakeOperator) int
	}{
		{"stop", func(f *fakeOperator) int { return len(f.stopped) }},
		{"start", func(f *fakeOperator) int { return len(f.started) }},
		{"restart", func(f *fakeOperator) int { return len(f.restarted) }},
	} {
		f := &fakeOperator{target: liveTarget()}
		code, out := ctlPost(t, f, "/api/containers/"+tc.verb,
			map[string]any{"source": "docker", "id": "abc123def456"})

		if code != http.StatusOK || out["ok"] != true {
			t.Fatalf("%s: code=%d out=%v", tc.verb, code, out)
		}
		if tc.want(f) != 1 {
			t.Errorf("%s: did not act", tc.verb)
		}
		// The answer names what was acted on, so a caller that addressed the
		// wrong row finds out from the response rather than from the machine.
		got, _ := out["container"].(map[string]any)
		if got["name"] != "db" || got["project"] != "someone-else" {
			t.Errorf("%s: container = %v, want the resolved one named back", tc.verb, got)
		}
	}
}

// TestNothingActsOnAnUnresolvedContainer is the property the Surface rests on:
// the resolve is not advisory, and a failure to resolve stops the request.
func TestNothingActsOnAnUnresolvedContainer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"missing container", container.ErrContainerMissing, http.StatusNotFound},
		{"missing source", container.ErrSourceMissing, http.StatusNotFound},
		// A real failure must not read as a refusal — nothing is known about
		// the machine's state after one.
		{"broken", errors.New("boom"), http.StatusInternalServerError},
	} {
		for _, verb := range []string{"stop", "start", "restart", "logs"} {
			f := &fakeOperator{resolveEr: tc.err}
			code, _ := ctlPost(t, f, "/api/containers/"+verb,
				map[string]any{"source": "docker", "id": "abc123def456"})
			if code != tc.want {
				t.Errorf("%s/%s: code = %d, want %d", tc.name, verb, code, tc.want)
			}
			if len(f.stopped)+len(f.started)+len(f.restarted)+len(f.logged) > 0 {
				t.Errorf("%s/%s: acted after a failed resolve", tc.name, verb)
			}
		}
	}
}

func TestControlRejectsIncompleteRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"no id", "/api/containers/stop", map[string]any{"source": "docker"}},
		{"no source", "/api/containers/stop", map[string]any{"id": "abc123def456"}},
		{"blank id", "/api/containers/stop", map[string]any{"source": "docker", "id": "  "}},
		// The verb set is closed at logs/stop/start/restart; devhub does not
		// remove containers, and a subcommand it never meant to offer cannot be
		// reached by naming it in the path.
		{"rm", "/api/containers/rm", map[string]any{"source": "docker", "id": "abc123def456"}},
		{"prune", "/api/containers/prune", map[string]any{"source": "docker", "id": "abc123def456"}},
		{"kill", "/api/containers/kill", map[string]any{"source": "docker", "id": "abc123def456"}},
	} {
		f := &fakeOperator{target: liveTarget()}
		if code, _ := ctlPost(t, f, tc.path, tc.body); code == http.StatusOK {
			t.Errorf("%s: accepted", tc.name)
		}
		if len(f.resolved) > 0 || len(f.stopped) > 0 || len(f.started) > 0 || len(f.restarted) > 0 {
			t.Errorf("%s: reached the operator", tc.name)
		}
	}
}

// TestStopOutlivesTheRequest, for the reason a resize does: exec.CommandContext
// kills the process when its context ends, and a container caught mid-stop is a
// worse place to leave things than either end of the operation.
func TestStopOutlivesTheRequest(t *testing.T) {
	f := &fakeOperator{target: liveTarget()}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/containers/stop", nil).WithContext(ctx)
	cancel()

	c := &Controller{control: f}
	if err := c.HandleControlPost(httptest.NewRecorder(), req,
		map[string]any{"source": "docker", "id": "abc123def456"}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if f.actCtxErr != nil {
		t.Errorf("ran under a cancelled context (%v); the stop would be killed", f.actCtxErr)
	}
}

func TestLogsPassTheTailThrough(t *testing.T) {
	f := &fakeOperator{target: liveTarget()}
	code, out := ctlPost(t, f, "/api/containers/logs",
		map[string]any{"source": "docker", "id": "abc123def456", "tail": float64(50)})

	if code != http.StatusOK {
		t.Fatalf("code = %d (%v)", code, out)
	}
	if len(f.logged) != 1 || f.logged[0] != 50 {
		t.Errorf("tail = %v, want [50]", f.logged)
	}
	if out["logs"] != "line one\nline two" {
		t.Errorf("logs = %v", out["logs"])
	}
	// A tail that is not a number falls back to the container package's
	// default rather than reaching it as one.
	f2 := &fakeOperator{target: liveTarget()}
	ctlPost(t, f2, "/api/containers/logs",
		map[string]any{"source": "docker", "id": "abc123def456", "tail": "all"})
	if len(f2.logged) != 1 || f2.logged[0] != 0 {
		t.Errorf("tail = %v, want [0] (the package default)", f2.logged)
	}
}
