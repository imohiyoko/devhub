// Package reqlog is devhub's in-memory record of the API requests it served.
//
// It exists because devhub could not previously show what an agent had done to
// the machine. The /ai-api approval prompt covers only the moment a write is
// first seen: a read never reaches it, and a write matching an always-allow rule
// short-circuits before a request is even registered. Once a user pressed
// "always allow", every later call through that rule was invisible.
//
// The log is deliberately volatile — a fixed ring in memory, gone when the
// process exits. What matters is being able to answer "what just happened", and
// that question is asked while devhub is still running. Anything worth keeping
// past a restart is copied out explicitly (see the logs controller's archive).
// Nothing here touches the disk, so recording a request costs no I/O on the
// request's own path.
package reqlog

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Capacity is how many entries the ring holds before the oldest is overwritten.
// Sized for "what happened in this session" rather than for retention: at the
// per-entry caps the caller applies (a redacted body summary, a short error
// excerpt) this is a few megabytes at worst, and usually far less.
const Capacity = 2000

// Approval outcomes recorded on an /ai-api write. Auto is the one that motivated
// this package: it is the case that previously left no trace anywhere.
const (
	ApprovalNone     = ""         // not an approval-gated request
	ApprovalAuto     = "auto"     // matched an always-allow rule, never prompted
	ApprovalManual   = "manual"   // the user approved it
	ApprovalRejected = "rejected" // the user declined it
	ApprovalTimeout  = "timeout"  // nobody answered in time
)

// Surface is the door a request arrived through. It is a named type rather than
// a plain string so Begin's three parameters cannot be transposed silently —
// they are otherwise interchangeable to the compiler, and a swapped surface and
// method would produce a log that reads plausibly and says the wrong thing.
type Surface string

// Surfaces a request can arrive on.
const (
	SurfaceAPI   Surface = "api"    // token-authenticated, i.e. devhub's own pages
	SurfaceAIAPI Surface = "ai-api" // token-less local surface, i.e. agents and CLIs
)

// Entry is one served request. It is filled in two stages: Begin records what is
// known on arrival, Finish records the outcome.
type Entry struct {
	// Seq is a per-ring counter assigned when the finished entry is stored, so
	// it ascends in *completion* order: a write that waited a minute for
	// approval is numbered after the reads that overtook it while it waited.
	//
	// It is an identity, not a display order. Paired with the ring's instance it
	// is the archive's dedup key, which is what makes archiving overlapping
	// selections twice a no-op. Order by TS when the question is "what happened
	// when" — Query does.
	Seq int64 `json:"seq"`

	TS      time.Time `json:"ts"`
	Surface Surface   `json:"surface"`
	Method  string    `json:"method"`
	Path    string    `json:"path"`

	Status int   `json:"status"`
	DurMs  int64 `json:"dur_ms"`
	Bytes  int   `json:"bytes"`

	// Approval is one of the Approval* constants.
	Approval string `json:"approval,omitempty"`
	// Code is the machine-readable error code from the response, when it carried
	// one. Filtering on it is how "show me every approval timeout" works.
	Code string `json:"code,omitempty"`
	// Body is a redacted, truncated summary of the request body. The caller does
	// the redacting — this package never sees a raw body.
	Body string `json:"body,omitempty"`
	// Err is a short excerpt of the response body, kept only for failures.
	Err string `json:"err,omitempty"`
}

// Ring is a fixed-size, concurrency-safe buffer of the most recent entries.
// The zero value is not usable; call New.
type Ring struct {
	mu  sync.RWMutex
	buf []*Entry
	// next is the write cursor; it only ever advances, so next-1 is the newest
	// entry and next-len(buf) the oldest still held.
	next int64
	seq  int64
	// instance names the run these seqs belong to. It lives on the ring rather
	// than beside it because seq is only meaningful when paired with it: two
	// rings each number their entries from 1, so an archive that keyed rows on
	// seq alone — or on an instance held one level up, shared by both rings —
	// would read the second ring's entry 1 as a copy of the first's and drop it.
	instance string
}

// New returns an empty ring of the given capacity (Capacity when n <= 0),
// labelled with instance. Give each ring an id no other ring will produce; the
// archive uses (instance, seq) as the identity of a request.
func New(n int, instance string) *Ring {
	if n <= 0 {
		n = Capacity
	}
	return &Ring{buf: make([]*Entry, n), instance: instance}
}

// Instance returns the id this ring labels its entries with.
func (rg *Ring) Instance() string { return rg.instance }

// Begin starts an entry for an arriving request. It does not touch the ring —
// nothing is recorded until Add — so a handler that panics leaves no half-built
// entry behind.
//
// It takes the pieces rather than the *http.Request because deciding what the
// recorded path should say is not this package's call: the caller normalizes
// the surface prefix away and redacts the query string before handing it over
// (see the server's requestLabel). Keeping that here would put secret-key
// heuristics inside the data structure.
func Begin(surface Surface, method, path string) *Entry {
	return &Entry{TS: time.Now(), Surface: surface, Method: method, Path: path}
}

// Finish records the outcome. errExcerpt should be empty for a success; the
// caller decides how much of a failure body is worth keeping.
func (e *Entry) Finish(status, bytes int, errExcerpt string, d time.Duration) {
	e.Status, e.Bytes, e.Err = status, bytes, errExcerpt
	e.DurMs = d.Milliseconds()
}

// Add stores a finished entry, overwriting the oldest when full, and assigns its
// Seq.
func (rg *Ring) Add(e *Entry) {
	if e == nil {
		return
	}
	rg.mu.Lock()
	defer rg.mu.Unlock()
	rg.seq++
	e.Seq = rg.seq
	rg.buf[rg.next%int64(len(rg.buf))] = e
	rg.next++
}

// Clear drops every entry. Seq keeps counting so an archived entry's key stays
// unique for the life of the process even across a clear.
func (rg *Ring) Clear() {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	rg.buf = make([]*Entry, len(rg.buf))
	rg.next = 0
}

// Filter selects entries. A zero-valued field means "do not filter on this",
// so the zero Filter matches everything.
type Filter struct {
	Since, Until time.Time
	Surface      Surface
	Method       string
	PathPrefix   string
	Approval     string
	Code         string
	StatusMin    int
	StatusMax    int
	MinDurMs     int64
	// Text matches case-insensitively against the path, body summary and error
	// excerpt — the catch-all for "I know roughly what I'm looking for".
	Text string
	// Limit caps the number of entries returned (0 = no cap).
	Limit int
}

// Match reports whether e satisfies every set condition.
func (f Filter) Match(e *Entry) bool {
	switch {
	case !f.Since.IsZero() && e.TS.Before(f.Since):
		return false
	case !f.Until.IsZero() && e.TS.After(f.Until):
		return false
	case f.Surface != "" && e.Surface != f.Surface:
		return false
	case f.Method != "" && !strings.EqualFold(e.Method, f.Method):
		return false
	case f.PathPrefix != "" && !strings.HasPrefix(e.Path, f.PathPrefix):
		return false
	case f.Approval != "" && e.Approval != f.Approval:
		return false
	case f.Code != "" && e.Code != f.Code:
		return false
	case f.StatusMin != 0 && e.Status < f.StatusMin:
		return false
	case f.StatusMax != 0 && e.Status > f.StatusMax:
		return false
	case f.MinDurMs != 0 && e.DurMs < f.MinDurMs:
		return false
	case f.Text != "" && !containsFold(e, f.Text):
		return false
	}
	return true
}

func containsFold(e *Entry, needle string) bool {
	n := strings.ToLower(needle)
	return strings.Contains(strings.ToLower(e.Path), n) ||
		strings.Contains(strings.ToLower(e.Body), n) ||
		strings.Contains(strings.ToLower(e.Err), n)
}

// Query returns matching entries, newest arrival first.
//
// The sort is on TS, not on Seq, and the whole match set is sorted before Limit
// truncates it. Seq is completion order, so ordering by it would put a write
// that blocked 60 seconds on approval *above* the reads that came in while it
// waited — the rows most worth looking at, displayed in the one order that
// misleads. Applying Limit first would have the same effect at the boundary,
// dropping a newer entry in favour of an older one that merely finished later.
//
// Ties break on Seq descending, matching the archive's ORDER BY ts DESC, seq
// DESC, so the same entries read the same way in both views.
func (rg *Ring) Query(f Filter) []Entry {
	rg.mu.RLock()
	defer rg.mu.RUnlock()

	n := int64(len(rg.buf))
	oldest := max(rg.next-n, 0)

	out := []Entry{}
	for i := rg.next - 1; i >= oldest; i-- {
		e := rg.buf[i%n]
		if e == nil || !f.Match(e) {
			continue
		}
		out = append(out, *e)
	}
	slices.SortStableFunc(out, func(a, b Entry) int {
		if c := b.TS.Compare(a.TS); c != 0 {
			return c
		}
		return cmp.Compare(b.Seq, a.Seq)
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// Len reports how many entries the ring currently holds.
func (rg *Ring) Len() int {
	rg.mu.RLock()
	defer rg.mu.RUnlock()
	return int(min(rg.next, int64(len(rg.buf))))
}
