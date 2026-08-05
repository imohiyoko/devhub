package reqlog

import (
	"net/http/httptest"
	"testing"
	"time"
)

func add(rg *Ring, e Entry) *Entry {
	cp := e
	if cp.TS.IsZero() {
		cp.TS = time.Now()
	}
	rg.Add(&cp)
	return &cp
}

func paths(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

func TestQueryReturnsNewestFirst(t *testing.T) {
	rg := New(10, "run-1")
	for _, p := range []string{"/api/a", "/api/b", "/api/c"} {
		add(rg, Entry{Path: p, Surface: SurfaceAPI})
	}

	got := paths(rg.Query(Filter{}))
	want := []string{"/api/c", "/api/b", "/api/a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// Seq is completion order, so an approval-gated write is stored after the reads
// that overtook it while it waited. Ordering the display by Seq would put that
// write — the row most worth reading — above requests that arrived later,
// contradicting the timestamps printed beside it. Query sorts on TS instead.
func TestQueryOrdersByArrivalNotCompletion(t *testing.T) {
	rg := New(10, "run-1")
	base := time.Now()

	// Arrival order is slow, quick. Completion order — and so Seq — is the
	// reverse, because the write blocked on approval.
	add(rg, Entry{Path: "/api/quick-read", TS: base.Add(time.Second)})
	add(rg, Entry{Path: "/ai-api/slow-write", TS: base})

	if got := paths(rg.Query(Filter{})); got[0] != "/api/quick-read" {
		t.Errorf("order = %v, want the later arrival first", got)
	}
	// Limit must not reintroduce it: truncating before the sort would keep the
	// highest Seq, which here is the older request.
	if got := paths(rg.Query(Filter{Limit: 1})); got[0] != "/api/quick-read" {
		t.Errorf("Limit 1 = %v, want the newest arrival", got)
	}
}

// Overflow must drop the oldest entries, not the newest: the reason to look at
// this log is almost always "what just happened".
func TestRingOverwritesOldest(t *testing.T) {
	rg := New(3, "run-1")
	for _, p := range []string{"/1", "/2", "/3", "/4", "/5"} {
		add(rg, Entry{Path: p})
	}

	if rg.Len() != 3 {
		t.Errorf("Len = %d, want 3", rg.Len())
	}
	got := paths(rg.Query(Filter{}))
	want := []string{"/5", "/4", "/3"}
	if len(got) != 3 {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// Seq must stay unique for the life of the process: the archive keys on it, so
// a repeat would silently drop a row under INSERT OR IGNORE.
func TestSeqIsMonotonicAcrossOverflowAndClear(t *testing.T) {
	rg := New(2, "run-1")
	seen := map[int64]bool{}
	record := func() {
		e := add(rg, Entry{Path: "/x"})
		if seen[e.Seq] {
			t.Fatalf("duplicate Seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
	for range 5 {
		record()
	}
	rg.Clear()
	if rg.Len() != 0 {
		t.Fatalf("Len after Clear = %d, want 0", rg.Len())
	}
	for range 3 {
		record()
	}
}

func TestFilters(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rg := New(20, "run-1")
	add(rg, Entry{TS: base, Path: "/api/ports", Surface: SurfaceAPI, Method: "GET", Status: 200, DurMs: 3})
	add(rg, Entry{TS: base.Add(time.Minute), Path: "/api/settings", Surface: SurfaceAIAPI, Method: "POST",
		Status: 408, DurMs: 60000, Approval: ApprovalTimeout, Code: "approval_timeout", Body: `{"editor":"code"}`})
	add(rg, Entry{TS: base.Add(2 * time.Minute), Path: "/api/git/status", Surface: SurfaceAIAPI, Method: "GET",
		Status: 500, DurMs: 12, Err: "boom"})

	for _, tc := range []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"no filter", Filter{}, []string{"/api/git/status", "/api/settings", "/api/ports"}},
		{"surface", Filter{Surface: SurfaceAPI}, []string{"/api/ports"}},
		{"method is case-insensitive", Filter{Method: "post"}, []string{"/api/settings"}},
		{"path prefix", Filter{PathPrefix: "/api/git"}, []string{"/api/git/status"}},
		{"approval", Filter{Approval: ApprovalTimeout}, []string{"/api/settings"}},
		{"code", Filter{Code: "approval_timeout"}, []string{"/api/settings"}},
		{"status floor", Filter{StatusMin: 400}, []string{"/api/git/status", "/api/settings"}},
		{"status range", Filter{StatusMin: 400, StatusMax: 499}, []string{"/api/settings"}},
		{"min duration", Filter{MinDurMs: 1000}, []string{"/api/settings"}},
		{"since", Filter{Since: base.Add(90 * time.Second)}, []string{"/api/git/status"}},
		{"until", Filter{Until: base.Add(30 * time.Second)}, []string{"/api/ports"}},
		{"text hits body", Filter{Text: "editor"}, []string{"/api/settings"}},
		{"text hits err", Filter{Text: "BOOM"}, []string{"/api/git/status"}},
		{"text hits path", Filter{Text: "ports"}, []string{"/api/ports"}},
		{"combined", Filter{Surface: SurfaceAIAPI, StatusMin: 500}, []string{"/api/git/status"}},
		{"no match", Filter{Code: "nope"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := paths(rg.Query(tc.filter))
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

func TestLimitTakesTheNewest(t *testing.T) {
	rg := New(10, "run-1")
	for _, p := range []string{"/1", "/2", "/3", "/4"} {
		add(rg, Entry{Path: p})
	}

	got := paths(rg.Query(Filter{Limit: 2}))
	if len(got) != 2 || got[0] != "/4" || got[1] != "/3" {
		t.Errorf("got %v, want [/4 /3]", got)
	}
}

func TestBeginClassifiesSurface(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/api/ports", SurfaceAPI},
		{"/ai-api/ports", SurfaceAIAPI},
	} {
		e := Begin(httptest.NewRequest("GET", tc.path, nil))
		if e.Surface != tc.want {
			t.Errorf("Begin(%q).Surface = %q, want %q", tc.path, e.Surface, tc.want)
		}
		if e.TS.IsZero() {
			t.Errorf("Begin(%q) left TS unset", tc.path)
		}
	}
}

// Begin must not publish anything: a request that dies mid-handler should leave
// no entry, and only Add makes one visible.
func TestBeginDoesNotRecord(t *testing.T) {
	rg := New(4, "run-1")
	e := Begin(httptest.NewRequest("GET", "/api/x", nil))
	if rg.Len() != 0 {
		t.Fatal("Begin wrote to the ring")
	}
	e.Finish(200, 10, "", 5*time.Millisecond)
	rg.Add(e)
	if rg.Len() != 1 {
		t.Fatal("Add did not record the entry")
	}
	if got := rg.Query(Filter{})[0]; got.Status != 200 || got.DurMs != 5 || got.Bytes != 10 {
		t.Errorf("Finish did not carry through: %+v", got)
	}
}

func TestConcurrentAddAndQuery(t *testing.T) {
	rg := New(64, "run-1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			add(rg, Entry{Path: "/api/x"})
		}
	}()
	for range 500 {
		rg.Query(Filter{Limit: 5})
	}
	<-done
}
