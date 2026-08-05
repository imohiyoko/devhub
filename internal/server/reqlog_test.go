package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/approval"
	"github.com/imohiyoko/devhub/internal/httpx"
	"github.com/imohiyoko/devhub/internal/reqlog"
	"github.com/imohiyoko/devhub/internal/storage"
)

// loggablePath sees the normalized path, so /ai-api never reaches it as such —
// that is the point of normalizing before the check, and why each rule below
// appears once rather than twice. TestRequestLabel covers the normalization
// itself; TestAssetsAndLogEndpointAreNotLogged covers the two ends together.
func TestLoggablePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/api/ports", true},
		{"/api/git/status", true},
		// Not the API: pages and assets are the bulk of the traffic and say
		// nothing about what was done to the machine.
		{"/", false},
		{"/git", false},
		{"/shared/net.js", false},
		{"/tools/git/git.css", false},
		// Reading the log is excluded, or searching would fill the log with
		// records of the searching and evict what the page exists to show.
		{"/api/logs", false},
		// Changing it is not excluded. These are the two operations that alter
		// the record itself, and leaving them out let a wipe pass for a quiet
		// hour.
		{"/api/logs/clear", true},
		{"/api/logs/archive", true},
	} {
		if got := loggablePath(tc.path); got != tc.want {
			t.Errorf("loggablePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// The recorded path answers "what was done", and for the side-effecting GETs
// the whole answer is in the query string. It also drops the /ai-api prefix, so
// one path filter covers both surfaces — the surface column already says which
// door was used.
func TestRequestLabel(t *testing.T) {
	for _, tc := range []struct {
		in          string
		wantSurface reqlog.Surface
		wantLabel   string
	}{
		{"/api/ports", reqlog.SurfaceAPI, "/api/ports"},
		{"/ai-api/git/status", reqlog.SurfaceAIAPI, "/api/git/status"},
		// The case the log existed for and could not answer — and the reason the
		// escaping is minimal: this is what a human reads in the prompt.
		{"/ai-api/open?path=%2Fetc", reqlog.SurfaceAIAPI, "/api/open?path=/etc"},
		{"/ai-api/open?path=%2FUsers%2Fme%2F%E4%BB%95%E4%BA%8B", reqlog.SurfaceAIAPI, "/api/open?path=/Users/me/仕事"},
		// Sorted, so an always-allow rule cannot depend on send order.
		{"/api/ls?b=2&a=1", reqlog.SurfaceAPI, "/api/ls?a=1&b=2"},
		// Same key heuristic as the body summary — and the mask stays legible,
		// so "show me the redacted ones" is a search rather than a guess.
		{"/api/x?token=abc&q=1", reqlog.SurfaceAPI, "/api/x?q=1&token=***"},
		// Injectivity: the separators and the escape character must not survive
		// as themselves, or two different queries could render identically.
		{"/api/x?a=1%26b=2", reqlog.SurfaceAPI, "/api/x?a=1%26b%3D2"},
		{"/api/x?a=100%25", reqlog.SurfaceAPI, "/api/x?a=100%25"},
		// A space would otherwise manufacture the boundary detailMatchesPattern
		// anchors on, letting one rule cover a request the user never saw.
		{"/api/x?a=b+c", reqlog.SurfaceAPI, "/api/x?a=b%20c"},
		// The path gets the same treatment. r.URL.Path arrives decoded, so
		// without this a request could put that same space into the detail.
		{"/ai-api/db/%20query", reqlog.SurfaceAIAPI, "/api/db/%20query"},
		// And "?" in a path must not be able to imitate a query that was never
		// sent — these two must not render alike.
		{"/api/x%3Fa=1", reqlog.SurfaceAPI, "/api/x%3Fa%3D1"},
		{"/api/x?a=1", reqlog.SurfaceAPI, "/api/x?a=1"},
		// Nothing recoverable: the bytes are dropped rather than echoed into a
		// log that gets archived, and the placeholder standing in for them
		// carries no space — it sits mid-detail, where a space is a boundary.
		{"/api/x?%zz", reqlog.SurfaceAPI, "/api/x?(unparseable-query)"},
	} {
		surface, path, query := requestLabel(httptest.NewRequest(http.MethodGet, tc.in, nil))
		if got := path + query; surface != tc.wantSurface || got != tc.wantLabel {
			t.Errorf("requestLabel(%q) = %q %q, want %q %q", tc.in, surface, got, tc.wantSurface, tc.wantLabel)
		}
	}
}

// labelEscape promises injectivity, and invalid UTF-8 is where that promise is
// easiest to break: ranging over a string yields U+FFFD for every bad byte, so
// two different requests would produce one label — and an always-allow rule for
// either would cover both. url.ParseQuery hands raw bytes through, so this is
// reachable from a plain HTTP client.
func TestLabelDistinguishesInvalidUTF8(t *testing.T) {
	label := func(target string) string {
		_, path, query := requestLabel(httptest.NewRequest(http.MethodGet, target, nil))
		return path + query
	}
	if a, b := label("/api/x?a=%FF"), label("/api/x?a=%FE"); a == b {
		t.Errorf("two different queries render alike: %q", a)
	}
	if a, b := label("/api/%FF"), label("/api/%FE"); a == b {
		t.Errorf("two different paths render alike: %q", a)
	}
}

// Recording the query only tightens anything if an always-allow rule cannot
// reach across it. Two separate mechanisms hold that up, and they fail in
// different ways, so both are asserted here through the real matcher.
func TestARuleDoesNotReachAcrossAQueryString(t *testing.T) {
	const action = "api_write"

	// 1. A rule stored before queries were part of a detail. It diverges from a
	// query-bearing detail at the character after the path — "(" against "?" —
	// so it is not even a prefix of one.
	t.Run("pre-existing rule", func(t *testing.T) {
		srv := newTestServer(t)
		srv.approvalMgr.AddAlwaysAllowRule(action, "GET /ai-api/open (no request body)")

		for _, detail := range []string{
			"GET /ai-api/open?path=/a (no request body)",
			"GET /ai-api/open?path=/b (no request body)",
		} {
			if srv.approvalMgr.ShouldAutoApprove(action, detail) {
				t.Errorf("still auto-approves %q", detail)
			}
		}
		if !srv.approvalMgr.ShouldAutoApprove(action, "GET /ai-api/open (no request body)") {
			t.Error("the rule no longer matches its own request")
		}
	})

	// 2. A pattern that ends inside the query. Here prefix matching alone would
	// say yes — "?path=/a" is a prefix of "?path=/abc" — and the only thing
	// saying no is the space anchor in detailMatchesPattern. Approving one
	// directory would otherwise approve every directory whose name extends it.
	//
	// This is also why labelEscape masks spaces: a value allowed to contain one
	// could manufacture the boundary the anchor looks for.
	//
	// The current API cannot produce a pattern of this shape — always-allow
	// stores a pending request's complete detail, and nothing else writes a
	// rule. What this guards is the rules file: patterns already persisted by an
	// older devhub, one edited by hand, and any future UI that lets a pattern be
	// shortened. Unreachable from the API is not the same as unreachable.
	t.Run("pattern ending inside the query", func(t *testing.T) {
		srv := newTestServer(t)
		srv.approvalMgr.AddAlwaysAllowRule(action, "GET /ai-api/open?path=/a")

		for _, detail := range []string{
			"GET /ai-api/open?path=/abc (no request body)",
			"GET /ai-api/open?path=/a%20b (no request body)",
		} {
			if srv.approvalMgr.ShouldAutoApprove(action, detail) {
				t.Errorf("a rule for /a reaches %q", detail)
			}
		}
	})
}

// Clearing and archiving the log are withheld from /ai-api outright, not merely
// gated on approval: approval is one "always allow" away from automatic, and
// the rule that made it automatic would be erased along with everything else.
func TestLogMutationsAreRefusedOnAiAPI(t *testing.T) {
	for _, path := range []string{"/ai-api/logs/clear", "/ai-api/logs/archive"} {
		srv := newTestServer(t)
		srv.do(http.MethodGet, "/api/ports", goodHost, testToken, "", nil) // something to lose

		rr := srv.do(http.MethodPost, path, goodHost, "", "", nil)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403: %s", path, rr.Code, rr.Body.String())
		}
		if m := decodeBodyMap(t, rr); m["code"] != "no_ai_api_route" {
			t.Errorf("%s: code = %v, want no_ai_api_route", path, m["code"])
		}
		// The handler must not have run on the way to saying no, and its effect
		// is the only way to observe that: had clear run, the entry from the
		// setup request above would be gone and only this refusal would remain.
		if n := srv.rlog.Len(); n < 2 {
			t.Errorf("%s: ring holds %d entries — the request was served, not refused", path, n)
		}
		// And the attempt is itself on the record.
		var found bool
		for _, e := range srv.rlog.Query(reqlog.Filter{Code: "no_ai_api_route"}) {
			found = found || e.Path == "/api/"+strings.TrimPrefix(path, "/ai-api/")
		}
		if !found {
			t.Errorf("%s: the refused attempt was not logged", path)
		}
	}
}

// A live clear is recorded even though it wipes the ring: ServeHTTP adds the
// entry after the handler returns, so the one line saying who cleared it
// survives its own wipe. Without that, a wipe is indistinguishable from an
// hour in which nothing happened.
func TestClearLeavesItsOwnRecord(t *testing.T) {
	srv := newTestServer(t)
	for range 3 {
		srv.do(http.MethodGet, "/api/ports", goodHost, testToken, "", nil)
	}
	if n := srv.rlog.Len(); n != 3 {
		t.Fatalf("setup: ring holds %d, want 3", n)
	}

	if rr := srv.do(http.MethodPost, "/api/logs/clear", goodHost, testToken, "", nil); rr.Code != http.StatusOK {
		t.Fatalf("clear = %d: %s", rr.Code, rr.Body.String())
	}

	entries := srv.rlog.Query(reqlog.Filter{})
	if len(entries) != 1 || entries[0].Path != "/api/logs/clear" {
		t.Errorf("after clear the ring holds %+v, want only the clear itself", entries)
	}
}

// Wrapping the ResponseWriter must not hide the interfaces handlers reach for.
// /api/restart, /api/rebuild and /api/update/apply flush their acknowledgement
// through a w.(http.Flusher) assertion and then replace the process, so a
// wrapper without Flush loses the reply entirely.
func TestStatusRecorderStaysAFlusher(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}

	f, ok := any(rec).(http.Flusher)
	if !ok {
		t.Fatal("statusRecorder is not an http.Flusher; handlers that flush before re-exec will silently stop flushing")
	}
	f.Flush() // must not panic when the wrapped writer flushes
}

// The same guard from the outside: the handler must observe a Flusher through
// the real serving path, not just when constructed directly.
func TestFlusherReachesHandlersThroughServeHTTP(t *testing.T) {
	srv := newTestServer(t)
	var sawFlusher bool

	// A recorder is an http.Flusher, so if the wrapper forwards the capability
	// the handler sees one.
	srv.gateway.Next = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, sawFlusher = w.(http.Flusher)
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	srv.do(http.MethodGet, "/api/does-not-exist", goodHost, testToken, "", nil)

	if !sawFlusher {
		t.Error("the handler did not see an http.Flusher")
	}
}

// hijackableWriter is a ResponseWriter that can be hijacked, which httptest's
// recorder cannot. Without it the test below would pass for the wrong reason:
// the controller would report ErrNotSupported because nothing underneath
// supports hijacking, rather than because statusRecorder refuses to hand the
// connection over.
type hijackableWriter struct{ http.ResponseWriter }

func (hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }

// The counterpart to the two above: Flush is forwarded on purpose, Hijack is
// withheld on purpose.
//
// Forwarding it — by giving statusRecorder an Unwrap method, which is all it
// would take — reads like harmless future-proofing. It is not. A hijacked
// connection leaves this recorder behind: no WriteHeader, no Write, and the
// ring ends up holding "200, 0 bytes" for a request that did something else
// entirely. ErrNotSupported stops whoever adds streaming and makes them say
// what the log should record; a fabricated entry stops no one, because nothing
// about it looks wrong.
//
// This test fails the moment an Unwrap method appears. That is its purpose.
func TestHijackIsRefusedRatherThanUnlogged(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: hijackableWriter{httptest.NewRecorder()}, status: http.StatusOK}

	if _, _, err := http.NewResponseController(rec).Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("Hijack() error = %v, want http.ErrNotSupported — a hijacked request would be logged as a clean 200", err)
	}
}

func TestRequestIsLoggedWithOutcome(t *testing.T) {
	srv := newTestServer(t)
	srv.do(http.MethodGet, "/api/ports", goodHost, testToken, "", nil)

	entries := srv.rlog.Query(reqlog.Filter{})
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Method != "GET" || e.Path != "/api/ports" {
		t.Errorf("method/path = %s %s", e.Method, e.Path)
	}
	if e.Surface != reqlog.SurfaceAPI {
		t.Errorf("surface = %q, want api", e.Surface)
	}
	if e.Status != http.StatusOK {
		t.Errorf("status = %d, want 200", e.Status)
	}
	if e.Bytes == 0 {
		t.Error("bytes not counted")
	}
	if e.TS.IsZero() {
		t.Error("timestamp not set")
	}
}

func TestAssetsAndLogEndpointAreNotLogged(t *testing.T) {
	srv := newTestServer(t)
	for _, target := range []string{"/", "/shared/net.js", "/api/logs"} {
		srv.do(http.MethodGet, target, goodHost, testToken, "", nil)
	}
	if n := srv.rlog.Len(); n != 0 {
		t.Errorf("logged %d entries, want 0: %+v", n, srv.rlog.Query(reqlog.Filter{}))
	}
}

// A failing response must carry its stable code into the log, so "show me every
// approval timeout" is a filter rather than a substring hunt.
func TestFailureRecordsCodeAndExcerpt(t *testing.T) {
	srv := newTestServer(t)
	srv.do(http.MethodGet, "/api/ports", goodHost, "", "", nil) // no token → 401

	entries := srv.rlog.Query(reqlog.Filter{})
	if len(entries) != 1 {
		t.Fatalf("logged %d entries, want 1", len(entries))
	}
	if entries[0].Code != "missing_token" {
		t.Errorf("code = %q, want missing_token", entries[0].Code)
	}
	if !strings.Contains(entries[0].Err, "unauthorized") {
		t.Errorf("err excerpt = %q", entries[0].Err)
	}
}

// A successful response must NOT be captured: the API returns things like whole
// git diffs, and keeping them would blow the ring's memory budget.
func TestSuccessBodyIsNotCaptured(t *testing.T) {
	srv := newTestServer(t)
	srv.do(http.MethodGet, "/api/info", goodHost, testToken, "", nil)

	e := srv.rlog.Query(reqlog.Filter{})[0]
	if e.Err != "" {
		t.Errorf("captured a successful response body: %q", e.Err)
	}
	if e.Bytes == 0 {
		t.Error("byte count should still be recorded for a success")
	}
}

// The whole point of the log is that a user can see what an agent did. A secret
// in a request body must not become the price of that visibility.
func TestLoggedBodyIsRedacted(t *testing.T) {
	srv := newTestServer(t)
	srv.do(http.MethodPost, "/api/settings", goodHost, testToken, "",
		strings.NewReader(`{"editor":"code","token":"s3cr3t-value"}`))

	e := srv.rlog.Query(reqlog.Filter{})[0]
	if strings.Contains(e.Body, "s3cr3t-value") {
		t.Fatalf("secret leaked into the request log: %q", e.Body)
	}
	if !strings.Contains(e.Body, "***") {
		t.Errorf("body = %q, want the secret masked", e.Body)
	}
	if !strings.Contains(e.Body, "editor") {
		t.Errorf("body = %q, want non-secret fields kept so it stays searchable", e.Body)
	}
}

// The reason this package exists: an always-allow rule short-circuits before a
// request is even registered, so before the log this call happened with no
// record anywhere.
func TestAutoApprovedWriteIsRecorded(t *testing.T) {
	srv := newTestServer(t)
	body := `{"port":3000,"label":"x"}`

	// First call: approve manually and note the detail the rule will match.
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "", strings.NewReader(body))
	}()
	deadline := time.Now().Add(2 * time.Second)
	var detail string
	for time.Now().Before(deadline) && detail == "" {
		for _, req := range srv.approvalMgr.ListPending() {
			detail = req.Detail
			srv.approvalMgr.AddAlwaysAllowRule(req.Action, req.Detail)
			_ = srv.approvalMgr.Respond(req.ID, approval.Approved)
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-done
	if detail == "" {
		t.Fatal("no approval request appeared")
	}

	// Second, identical call: the rule matches, so nothing is prompted.
	srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "", strings.NewReader(body))

	auto := srv.rlog.Query(reqlog.Filter{Approval: reqlog.ApprovalAuto})
	if len(auto) != 1 {
		t.Fatalf("auto-approved entries = %d, want 1; log: %+v", len(auto), srv.rlog.Query(reqlog.Filter{}))
	}
	if auto[0].Surface != reqlog.SurfaceAIAPI {
		t.Errorf("surface = %q, want ai-api", auto[0].Surface)
	}

	manual := srv.rlog.Query(reqlog.Filter{Approval: reqlog.ApprovalManual})
	if len(manual) != 1 {
		t.Errorf("manual entries = %d, want 1", len(manual))
	}
}

func TestApprovalOutcomesAreRecorded(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		srv := newTestServer(t)
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "",
				strings.NewReader(`{"port":3000,"label":"x"}`))
		}()
		approve(t, srv, approval.Rejected)
		<-done

		if got := srv.rlog.Query(reqlog.Filter{Approval: reqlog.ApprovalRejected}); len(got) != 1 {
			t.Fatalf("rejected entries = %d, want 1", len(got))
		}
	})

	t.Run("timeout", func(t *testing.T) {
		srv := newTestServer(t)
		orig := approvalTimeout
		approvalTimeout = 10 * time.Millisecond
		t.Cleanup(func() { approvalTimeout = orig })

		srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "",
			strings.NewReader(`{"port":3000,"label":"x"}`))

		got := srv.rlog.Query(reqlog.Filter{Approval: reqlog.ApprovalTimeout})
		if len(got) != 1 {
			t.Fatalf("timeout entries = %d, want 1", len(got))
		}
		if got[0].Code != "approval_timeout" {
			t.Errorf("code = %q, want approval_timeout", got[0].Code)
		}
	})
}

// The body has to survive being read for the log, or every write would reach its
// handler empty.
func TestCaptureBodyRestoresItForTheHandler(t *testing.T) {
	srv := newTestServer(t)
	rr := srv.do(http.MethodPost, "/api/ports/label", goodHost, testToken, "",
		strings.NewReader(`{"port":3000,"label":"kept"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	rr = srv.do(http.MethodGet, "/api/settings", goodHost, testToken, "", nil)
	if !strings.Contains(rr.Body.String(), "kept") {
		t.Error("the label never reached the handler — the body was consumed by logging")
	}
}

// Every ring numbers its entries from 1, so seq only identifies a request when
// paired with an id no other ring uses. Two servers sharing one id would make
// their first requests indistinguishable in the archive, and INSERT OR IGNORE
// would drop the second — reported as "already archived", not as a loss.
//
// Nothing in devhub builds two servers in one process today (startServer runs
// once and a restart re-execs), which is precisely why this is worth pinning:
// if that ever changes, no other test would notice the archive going quiet.
func TestTwoServersOverOneStoreArchiveSeparately(t *testing.T) {
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	a, b := newTestServerOn(t, st), newTestServerOn(t, st)
	if a.instance == b.instance {
		t.Fatal("both servers minted the same instance id")
	}

	for i, srv := range []*Server{a, b} {
		srv.do(http.MethodGet, "/api/ports", goodHost, testToken, "", nil)
		// The premise: both rings hand out seq 1, so only the id separates them.
		if entries := srv.rlog.Query(reqlog.Filter{}); len(entries) != 1 || entries[0].Seq != 1 {
			t.Fatalf("server %d: want one entry with seq 1, got %+v", i, entries)
		}
		if rr := srv.do(http.MethodPost, "/api/logs/archive", goodHost, testToken, "", nil); rr.Code != http.StatusOK {
			t.Fatalf("server %d: archive = %d: %s", i, rr.Code, rr.Body.String())
		}
	}

	rr := a.do(http.MethodGet, "/api/logs?source=archive", goodHost, testToken, "", nil)
	var got struct {
		Entries []struct {
			Seq int64 `json:"seq"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode archive: %v (%s)", err, rr.Body.String())
	}
	if len(got.Entries) != 2 {
		t.Errorf("archive holds %d entries, want 2 — one server's request was swallowed", len(got.Entries))
	}
}

// The approval detail is the string a user actually reads before deciding, and
// the one an "always allow" rule is then stored from. It is assembled in
// serve() — labelEscape(origPath) + redactedQuery — not by requestLabel, which
// is what TestRequestLabel covers.
//
// The two share redactedQuery today, which makes this look like a restatement
// of that test. It is not: nothing forces them to keep sharing it, and if they
// ever diverge, the copy that matters is this one. A token echoed into a prompt
// is a token echoed into a persisted rule.
//
// The path stays on /ai-api here rather than being normalized, because which
// door a request came through is part of what is being approved.
func TestApprovalDetailCarriesTheQueryWithSecretsMasked(t *testing.T) {
	srv := newTestServer(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.do(http.MethodGet, "/ai-api/open?token=abc&path=%2Ftmp%2Fx", goodHost, "", "", nil)
	}()

	var detail string
	deadline := time.Now().Add(2 * time.Second)
	for detail == "" && time.Now().Before(deadline) {
		for _, req := range srv.approvalMgr.ListPending() {
			detail = req.Detail
			if err := srv.approvalMgr.Respond(req.ID, approval.Rejected); err != nil {
				t.Errorf("Respond: %v", err)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-done

	if detail == "" {
		t.Fatal("no approval request appeared")
	}
	// Readable, not percent-encoded: this is the whole reason labelEscape is
	// minimal rather than url.Values.Encode.
	if !strings.Contains(detail, "?path=/tmp/x&") {
		t.Errorf("detail lost the query or over-encoded it: %q", detail)
	}
	if !strings.Contains(detail, "token=***") {
		t.Errorf("detail did not mask the token: %q", detail)
	}
	if strings.Contains(detail, "abc") {
		t.Errorf("detail leaked the secret value: %q", detail)
	}
	if !strings.HasPrefix(detail, "GET /ai-api/open?") {
		t.Errorf("detail should keep the surface it arrived on: %q", detail)
	}
}
