package logs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/storage"
)

// fakeStore stands in for the SQLite archive so the controller's own behaviour
// (what it selects, converts and reports) is tested without a database.
type fakeStore struct {
	rows      []storage.RequestLogRow
	lastQuery storage.RequestLogFilter
	lastDel   storage.RequestLogFilter
}

func (f *fakeStore) ArchiveRequestLogs(rows []storage.RequestLogRow) (int, error) {
	inserted := 0
	for _, r := range rows {
		if !f.has(r.Instance, r.Seq) {
			f.rows = append(f.rows, r)
			inserted++
		}
	}
	return inserted, nil
}

func (f *fakeStore) has(instance string, seq int64) bool {
	for _, r := range f.rows {
		if r.Instance == instance && r.Seq == seq {
			return true
		}
	}
	return false
}

func (f *fakeStore) QueryRequestLogArchive(flt storage.RequestLogFilter) ([]storage.RequestLogRow, error) {
	f.lastQuery = flt
	return f.rows, nil
}

func (f *fakeStore) DeleteRequestLogArchive(flt storage.RequestLogFilter) (int, error) {
	f.lastDel = flt
	n := len(f.rows)
	f.rows = nil
	return n, nil
}

func newCtl(t *testing.T) (*Controller, *reqlog.Ring, *fakeStore) {
	t.Helper()
	ring, store := reqlog.New(50, "inst-1"), &fakeStore{}
	return New(ring, store), ring, store
}

func add(ring *reqlog.Ring, e reqlog.Entry) {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	ring.Add(&e)
}

// call runs a handler and returns the decoded body. A handler error is written
// the same way the gateway writes it, so an error response is testable too.
func call(t *testing.T, h func(http.ResponseWriter, *http.Request) error, target string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	if err := h(rr, httptest.NewRequest(http.MethodGet, target, nil)); err != nil {
		httpx.WriteError(rr, err)
	}
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body.String(), err)
	}
	return rr, m
}

func entryCount(t *testing.T, m map[string]any) int {
	t.Helper()
	entries, ok := m["entries"].([]any)
	if !ok {
		t.Fatalf("no entries in %v", m)
	}
	return len(entries)
}

func TestGetDefaultsToLive(t *testing.T) {
	ctl, ring, _ := newCtl(t)
	add(ring, reqlog.Entry{Path: "/api/ports", Surface: reqlog.SurfaceAPI, Status: 200})

	_, m := call(t, ctl.HandleGet, "/api/logs")
	if m["source"] != "live" {
		t.Errorf("source = %v, want live", m["source"])
	}
	if entryCount(t, m) != 1 {
		t.Errorf("entries = %v", m["entries"])
	}
}

// The same filter parameters have to mean the same thing on both sources, or
// switching live→archive silently changes the question being asked.
func TestFilterParamsApplyToBothSources(t *testing.T) {
	ctl, ring, store := newCtl(t)
	add(ring, reqlog.Entry{Path: "/api/ports", Surface: reqlog.SurfaceAPI, Status: 200})
	add(ring, reqlog.Entry{Path: "/api/settings", Surface: reqlog.SurfaceAIAPI, Status: 408,
		Approval: reqlog.ApprovalTimeout, Code: "approval_timeout"})

	_, m := call(t, ctl.HandleGet, "/api/logs?surface=ai-api&status_min=400&code=approval_timeout")
	if entryCount(t, m) != 1 {
		t.Errorf("live filter selected %v", m["entries"])
	}

	call(t, ctl.HandleGet, "/api/logs?source=archive&surface=ai-api&status_min=400&code=approval_timeout&method=post")
	got := store.lastQuery
	if got.Surface != "ai-api" || got.StatusMin != 400 || got.Code != "approval_timeout" || got.Method != "post" {
		t.Errorf("archive filter = %+v, want the same conditions", got)
	}
}

func TestRelativeTimeFilter(t *testing.T) {
	ctl, ring, _ := newCtl(t)
	add(ring, reqlog.Entry{Path: "/old", TS: time.Now().Add(-time.Hour)})
	add(ring, reqlog.Entry{Path: "/new"})

	_, m := call(t, ctl.HandleGet, "/api/logs?since=-15m")
	if entryCount(t, m) != 1 {
		t.Errorf("since=-15m selected %v, want only the recent entry", m["entries"])
	}
}

func TestBadParamsAreRejectedWithHints(t *testing.T) {
	ctl, _, _ := newCtl(t)
	for _, tc := range []struct{ target, wantCode string }{
		{"/api/logs?source=nope", "bad_source"},
		{"/api/logs?surface=nope", "bad_surface"},
		{"/api/logs?since=yesterday", "bad_time"},
		{"/api/logs?status_min=abc", "bad_number"},
	} {
		rr, m := call(t, ctl.HandleGet, tc.target)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.target, rr.Code)
		}
		if m["code"] != tc.wantCode {
			t.Errorf("%s: code = %v, want %v", tc.target, m["code"], tc.wantCode)
		}
		if m["hint"] == nil {
			t.Errorf("%s: no hint", tc.target)
		}
	}
}

func TestArchiveCopiesMatchingEntries(t *testing.T) {
	ctl, ring, store := newCtl(t)
	add(ring, reqlog.Entry{Path: "/api/ports", Surface: reqlog.SurfaceAPI, Status: 200})
	add(ring, reqlog.Entry{Path: "/api/settings", Surface: reqlog.SurfaceAIAPI, Status: 500})

	_, m := call(t, ctl.HandleArchive, "/api/logs/archive?status_min=500")
	if m["archived"].(float64) != 1 {
		t.Fatalf("archived = %v, want 1", m["archived"])
	}
	if len(store.rows) != 1 || store.rows[0].Path != "/api/settings" {
		t.Fatalf("archived the wrong entry: %+v", store.rows)
	}
	if store.rows[0].Instance != "inst-1" {
		t.Errorf("instance = %q, want inst-1", store.rows[0].Instance)
	}
	if store.rows[0].TS == "" {
		t.Error("timestamp not converted for storage")
	}

	// Repeating it reports the skip rather than duplicating.
	_, m = call(t, ctl.HandleArchive, "/api/logs/archive?status_min=500")
	if m["archived"].(float64) != 0 || m["skipped"].(float64) != 1 {
		t.Errorf("second archive: archived=%v skipped=%v, want 0 and 1", m["archived"], m["skipped"])
	}
}

// Archiving is "keep this", not "show me a page", so it must not stop at the
// display limit.
func TestArchiveIgnoresDisplayLimit(t *testing.T) {
	ctl, ring, store := newCtl(t)
	for range 5 {
		add(ring, reqlog.Entry{Path: "/api/x", Status: 200})
	}

	call(t, ctl.HandleArchive, "/api/logs/archive?limit=2")
	if len(store.rows) != 5 {
		t.Errorf("archived %d rows, want all 5", len(store.rows))
	}
}

func TestClear(t *testing.T) {
	ctl, ring, store := newCtl(t)
	add(ring, reqlog.Entry{Path: "/api/x"})
	store.rows = []storage.RequestLogRow{{Instance: "inst-1", Seq: 1}}

	call(t, ctl.HandleClear, "/api/logs/clear")
	if ring.Len() != 0 {
		t.Error("live log not cleared")
	}
	if len(store.rows) != 1 {
		t.Error("clearing the live log must not touch the archive")
	}

	_, m := call(t, ctl.HandleClear, "/api/logs/clear?source=archive")
	if m["deleted"].(float64) != 1 {
		t.Errorf("deleted = %v, want 1", m["deleted"])
	}
}
