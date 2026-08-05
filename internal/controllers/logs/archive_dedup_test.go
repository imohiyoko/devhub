package logs

import (
	"net/http/httptest"
	"sync"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/storage"
)

// Archiving the same request twice is not an edge case, it is the normal way the
// feature is used: the button archives whatever the current filter selects, and
// filters overlap. What keeps the archive from growing a second copy is
// UNIQUE(instance, seq) plus INSERT OR IGNORE — a constraint in SQLite, which
// the fake store in logs_test.go only imitates. These tests therefore run
// against the real store.
//
// The failure they guard against is worse than a duplicate row. If (instance,
// seq) ever repeated across two *different* requests, INSERT OR IGNORE would
// discard the second one, and the controller would report it as "skipped" —
// indistinguishable from "already archived". A request would vanish quietly.

func realCtl(t *testing.T) (*Controller, *reqlog.Ring, *storage.Store) {
	t.Helper()
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ring := reqlog.New(50, "inst-1")
	return New(ring, st), ring, st
}

func archivedRows(t *testing.T, st *storage.Store) []storage.RequestLogRow {
	t.Helper()
	got, err := st.QueryRequestLogArchive(storage.RequestLogFilter{})
	if err != nil {
		t.Fatalf("QueryRequestLogArchive: %v", err)
	}
	return got
}

func doArchive(t *testing.T, ctl *Controller, query string) {
	t.Helper()
	rr := httptest.NewRecorder()
	if err := ctl.HandleArchive(rr, httptest.NewRequest("POST", "/api/logs/archive?"+query, nil)); err != nil {
		t.Fatalf("archive %q: %v", query, err)
	}
}

// Clearing the live log must not restart seq. If it did, the next request would
// reuse a seq that is already in the archive, and INSERT OR IGNORE would drop
// it — the user would lose a request by pressing a button labelled "clear the
// view".
func TestClearDoesNotRecycleSeq(t *testing.T) {
	ctl, ring, st := realCtl(t)

	add(ring, reqlog.Entry{Path: "/before-clear", Status: 200})
	doArchive(t, ctl, "")
	ring.Clear()
	add(ring, reqlog.Entry{Path: "/after-clear", Status: 200})
	doArchive(t, ctl, "")

	got := archivedRows(t, st)
	if len(got) != 2 {
		t.Fatalf("archive holds %d rows, want 2 — a post-clear request was swallowed: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.Path] = true
	}
	if !seen["/before-clear"] || !seen["/after-clear"] {
		t.Errorf("want both requests archived, got %+v", got)
	}
}

// Overlapping selections are the expected usage: archive the failures, then the
// ai-api calls, then everything. Each request must end up stored exactly once.
func TestOverlappingFiltersArchiveEachRequestOnce(t *testing.T) {
	ctl, ring, st := realCtl(t)
	add(ring, reqlog.Entry{Path: "/a", Status: 200, Surface: reqlog.SurfaceAPI})
	add(ring, reqlog.Entry{Path: "/b", Status: 500, Surface: reqlog.SurfaceAPI})
	add(ring, reqlog.Entry{Path: "/c", Status: 500, Surface: reqlog.SurfaceAIAPI})

	doArchive(t, ctl, "status_min=500") // b, c
	doArchive(t, ctl, "surface=api")    // a, b — b repeats
	doArchive(t, ctl, "")               // all three repeat
	doArchive(t, ctl, "surface=ai-api") // c repeats

	if got := archivedRows(t, st); len(got) != 3 {
		t.Errorf("archive holds %d rows, want 3: %+v", len(got), got)
	}
}

// A double-clicked button sends the same archive twice, so the transactions can
// overlap. The constraint has to hold there too, without either call erroring.
func TestConcurrentArchivesDoNotDuplicate(t *testing.T) {
	ctl, ring, st := realCtl(t)
	for range 20 {
		add(ring, reqlog.Entry{Path: "/api/x", Status: 500})
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/logs/archive?status_min=500", nil)
			if err := ctl.HandleArchive(rr, req); err != nil {
				t.Errorf("concurrent archive: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := archivedRows(t, st); len(got) != 20 {
		t.Errorf("archive holds %d rows, want exactly 20", len(got))
	}
}

// Two devhub runs each number their requests from 1, so seq alone cannot
// identify one. The instance half of the key is what keeps them apart — without
// it, the second run's first request would be discarded as a duplicate of the
// first run's.
func TestSameSeqFromDifferentRunsBothSurvive(t *testing.T) {
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	for _, instance := range []string{"run-a", "run-b"} {
		ring := reqlog.New(10, instance)
		ctl := New(ring, st)
		add(ring, reqlog.Entry{Path: "/" + instance, Status: 200})
		doArchive(t, ctl, "")
	}

	if got := archivedRows(t, st); len(got) != 2 {
		t.Errorf("archive holds %d rows, want one per run: %+v", len(got), got)
	}
}
