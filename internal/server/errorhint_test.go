package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/imohiyoko/devhub/internal/approval"
)

func decodeBodyMap(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", rr.Body.String(), err)
	}
	return m
}

// A caller that isn't a browser cannot obtain the injected token, so the 401
// has to name /ai-api — otherwise there is nothing in the response, or anywhere
// else it can reach, that reveals the agent surface exists.
func TestUnauthorizedNamesAiAPI(t *testing.T) {
	srv := newTestServer(t)
	rr := srv.do(http.MethodGet, "/api/ports", goodHost, "", "", nil)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	m := decodeBodyMap(t, rr)
	if m["code"] != "missing_token" {
		t.Errorf("code = %v, want missing_token", m["code"])
	}
	hint, _ := m["hint"].(string)
	if !strings.Contains(hint, "/ai-api/ports") {
		t.Errorf("hint does not point at the equivalent /ai-api route: %q", hint)
	}
	if !strings.Contains(hint, "X-Devhub-Agent-Token") {
		t.Errorf("hint does not explain the agent credential: %q", hint)
	}
}

// The approval endpoints have no /ai-api equivalent on purpose — a caller must
// not be able to approve its own pending write. The hint must not send one
// looking for a route that would be a hole if it existed.
// The bare /api/approval is in the list because it is not a route: it reaches
// the token check as an unknown path, and a prefix test written with a trailing
// slash lets exactly that one through with the generic hint.
func TestApprovalEndpointHintDoesNotOfferSelfApproval(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{
		"/api/approval",
		"/api/approval/pending",
		"/api/approval/respond",
		"/api/approval/always-allow",
		"/api/approval/rules",
		"/api/approval/rules/abc",
	} {
		rr := srv.do(http.MethodGet, path, goodHost, "", "", nil)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", path, rr.Code)
		}
		hint, _ := decodeBodyMap(t, rr)["hint"].(string)
		if strings.Contains(hint, "/ai-api/approval") {
			t.Errorf("%s: hint names a self-approval route that must not exist: %q", path, hint)
		}
		if !strings.Contains(hint, "Ask the user") {
			t.Errorf("%s: hint should send the caller to the user, got %q", path, hint)
		}
	}
}

// approve resolves the first pending approval request with d, polling until one
// shows up. The request is registered by the handler goroutine, so the test
// cannot know when it exists without waiting for it.
func approve(t *testing.T, srv *Server, d approval.Decision) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, req := range srv.approvalMgr.ListPending() {
			if err := srv.approvalMgr.Respond(req.ID, d); err != nil {
				t.Errorf("Respond: %v", err)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no approval request appeared")
}

// A rejection and a timeout ask the caller for opposite things — give up versus
// send it again — so they must not answer with the same status or code.
func TestApprovalRejectedIsDistinctFromTimeout(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		srv := newTestServer(t)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "",
				strings.NewReader(`{"port":3000,"label":"x"}`))
		}()
		approve(t, srv, approval.Rejected)

		rr := <-done
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rr.Code)
		}
		m := decodeBodyMap(t, rr)
		if m["code"] != "approval_rejected" {
			t.Errorf("code = %v, want approval_rejected", m["code"])
		}
		if hint, _ := m["hint"].(string); !strings.Contains(hint, "Do not retry") {
			t.Errorf("hint should tell the caller to stop, got %q", hint)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		srv := newTestServer(t)
		orig := approvalTimeout
		approvalTimeout = 10 * time.Millisecond
		t.Cleanup(func() { approvalTimeout = orig })

		rr := srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "",
			strings.NewReader(`{"port":3000,"label":"x"}`))

		if rr.Code != http.StatusRequestTimeout {
			t.Fatalf("status = %d, want 408", rr.Code)
		}
		m := decodeBodyMap(t, rr)
		if m["code"] != "approval_timeout" {
			t.Errorf("code = %v, want approval_timeout", m["code"])
		}
		// The timed-out request is already marked Rejected and gone from the
		// pending list, so the hint must say "send it again", not "approve the
		// one that's waiting" — there is nothing waiting.
		hint, _ := m["hint"].(string)
		if !strings.Contains(hint, "again") {
			t.Errorf("hint should tell the caller to resend, got %q", hint)
		}
		if len(srv.approvalMgr.ListPending()) != 0 {
			t.Error("timed-out request is still pending; the hint's premise is wrong")
		}
	})
}

// An approved write must still reach its handler: the split above must not have
// turned the success path into a rejection.
func TestApprovedWriteStillPassesThrough(t *testing.T) {
	srv := newTestServer(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "",
			strings.NewReader(`{"port":3000,"label":"x"}`))
	}()
	approve(t, srv, approval.Approved)

	if rr := <-done; rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}
