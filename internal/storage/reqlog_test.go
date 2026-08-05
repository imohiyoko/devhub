package storage

import (
	"testing"
	"time"

	devhub "github.com/imohiyoko/devhub"
)

func newLogStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func row(seq int64, path string, mut ...func(*RequestLogRow)) RequestLogRow {
	r := RequestLogRow{
		Instance: "inst-1",
		Seq:      seq,
		TS:       FormatLogTime(time.Date(2026, 8, 5, 12, int(seq), 0, 0, time.UTC)),
		Surface:  "ai-api",
		Method:   "GET",
		Path:     path,
		Status:   200,
	}
	for _, m := range mut {
		m(&r)
	}
	return r
}

func archivedPaths(t *testing.T, s *Store, f RequestLogFilter) []string {
	t.Helper()
	rows, err := s.QueryRequestLogArchive(f)
	if err != nil {
		t.Fatalf("QueryRequestLogArchive: %v", err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path
	}
	return out
}

func TestArchiveRoundTrip(t *testing.T) {
	s := newLogStore(t)
	in := row(1, "/api/settings", func(r *RequestLogRow) {
		r.Method, r.Status, r.DurMs, r.Bytes = "POST", 408, 60000, 42
		r.Approval, r.Code = "timeout", "approval_timeout"
		r.Body, r.Err = `{"editor":"code"}`, "approval timed out"
	})

	n, err := s.ArchiveRequestLogs([]RequestLogRow{in})
	if err != nil {
		t.Fatalf("ArchiveRequestLogs: %v", err)
	}
	if n != 1 {
		t.Fatalf("archived %d, want 1", n)
	}

	rows, err := s.QueryRequestLogArchive(RequestLogFilter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("read back %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.ArchivedAt == "" {
		t.Error("archived_at not stamped")
	}
	got.ArchivedAt = ""
	if got != in {
		t.Errorf("round trip changed the row:\n got %+v\nwant %+v", got, in)
	}
}

// Archiving overlapping selections must not duplicate rows: (instance, seq) is
// unique, so the second insert is ignored rather than doubling the entry.
func TestArchiveIsIdempotent(t *testing.T) {
	s := newLogStore(t)
	rows := []RequestLogRow{row(1, "/a"), row(2, "/b")}

	if n, _ := s.ArchiveRequestLogs(rows); n != 2 {
		t.Fatalf("first archive inserted %d, want 2", n)
	}
	// Same two, plus a new one — as if the user widened the filter and archived
	// again.
	n, err := s.ArchiveRequestLogs(append(rows, row(3, "/c")))
	if err != nil {
		t.Fatalf("second archive: %v", err)
	}
	if n != 1 {
		t.Errorf("second archive inserted %d, want 1 (the two repeats must be skipped)", n)
	}
	if got := archivedPaths(t, s, RequestLogFilter{}); len(got) != 3 {
		t.Errorf("archive holds %v, want 3 distinct rows", got)
	}
}

// A different process start reuses seq numbers from 1, so the instance is what
// keeps them apart.
func TestSameSeqFromAnotherInstanceIsKept(t *testing.T) {
	s := newLogStore(t)
	a := row(1, "/from-run-a")
	b := row(1, "/from-run-b")
	b.Instance = "inst-2"

	if _, err := s.ArchiveRequestLogs([]RequestLogRow{a, b}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := archivedPaths(t, s, RequestLogFilter{}); len(got) != 2 {
		t.Errorf("got %v, want both runs' entries", got)
	}
}

func TestArchiveFilters(t *testing.T) {
	s := newLogStore(t)
	_, err := s.ArchiveRequestLogs([]RequestLogRow{
		row(1, "/api/ports", func(r *RequestLogRow) { r.Surface = "api" }),
		row(2, "/api/settings", func(r *RequestLogRow) {
			r.Method, r.Status, r.DurMs = "POST", 408, 60000
			r.Approval, r.Code, r.Body = "timeout", "approval_timeout", `{"editor":"code"}`
		}),
		row(3, "/api/git/status", func(r *RequestLogRow) { r.Status, r.Err = 500, "boom" }),
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	for _, tc := range []struct {
		name   string
		filter RequestLogFilter
		want   []string
	}{
		{"none", RequestLogFilter{}, []string{"/api/git/status", "/api/settings", "/api/ports"}},
		{"surface", RequestLogFilter{Surface: "api"}, []string{"/api/ports"}},
		{"method is case-insensitive", RequestLogFilter{Method: "post"}, []string{"/api/settings"}},
		{"path prefix", RequestLogFilter{PathPrefix: "/api/git"}, []string{"/api/git/status"}},
		{"approval", RequestLogFilter{Approval: "timeout"}, []string{"/api/settings"}},
		{"code", RequestLogFilter{Code: "approval_timeout"}, []string{"/api/settings"}},
		{"status range", RequestLogFilter{StatusMin: 400, StatusMax: 499}, []string{"/api/settings"}},
		{"min duration", RequestLogFilter{MinDurMs: 1000}, []string{"/api/settings"}},
		{"text hits body", RequestLogFilter{Text: "editor"}, []string{"/api/settings"}},
		{"text hits err", RequestLogFilter{Text: "boom"}, []string{"/api/git/status"}},
		{"limit keeps newest", RequestLogFilter{Limit: 1}, []string{"/api/git/status"}},
		{"since", RequestLogFilter{Since: FormatLogTime(time.Date(2026, 8, 5, 12, 2, 30, 0, time.UTC))},
			[]string{"/api/git/status"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := archivedPaths(t, s, tc.filter)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// LIKE wildcards in a search term must be literal, or searching for "100%"
// silently matches everything.
func TestTextFilterEscapesWildcards(t *testing.T) {
	s := newLogStore(t)
	if _, err := s.ArchiveRequestLogs([]RequestLogRow{
		row(1, "/api/a", func(r *RequestLogRow) { r.Body = "100% done" }),
		row(2, "/api/b", func(r *RequestLogRow) { r.Body = "nothing to see" }),
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	if got := archivedPaths(t, s, RequestLogFilter{Text: "100%"}); len(got) != 1 || got[0] != "/api/a" {
		t.Errorf("got %v, want only /api/a", got)
	}
	if got := archivedPaths(t, s, RequestLogFilter{Text: "_"}); len(got) != 0 {
		t.Errorf(`"_" matched %v; it should be a literal underscore, not a wildcard`, got)
	}
}

func TestDeleteArchive(t *testing.T) {
	s := newLogStore(t)
	if _, err := s.ArchiveRequestLogs([]RequestLogRow{
		row(1, "/api/ports", func(r *RequestLogRow) { r.Surface = "api" }),
		row(2, "/api/settings"),
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	n, err := s.DeleteRequestLogArchive(RequestLogFilter{Surface: "api"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	if got := archivedPaths(t, s, RequestLogFilter{}); len(got) != 1 || got[0] != "/api/settings" {
		t.Errorf("remaining = %v, want [/api/settings]", got)
	}

	if _, err := s.DeleteRequestLogArchive(RequestLogFilter{}); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	if got := archivedPaths(t, s, RequestLogFilter{}); len(got) != 0 {
		t.Errorf("an empty filter should clear the archive, left %v", got)
	}
}

func TestArchiveEmptyIsNoop(t *testing.T) {
	s := newLogStore(t)
	if n, err := s.ArchiveRequestLogs(nil); err != nil || n != 0 {
		t.Errorf("ArchiveRequestLogs(nil) = %d, %v; want 0, nil", n, err)
	}
}
