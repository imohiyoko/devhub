package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/imohiyoko/devhub/internal/approval"
)

// paddedLabelBody builds a body whose summary is cut before "port": the padding
// key sorts first, so whatever follows it is not shown in the prompt.
func paddedLabelBody(port string) string {
	return `{"a":"` + strings.Repeat("x", 1000) + `","label":"x","port":` + port + `}`
}

// A truncated detail does not say what is written: two bodies that differ only
// past the cut share it. "Always allow" on such a request would approve the
// other body without the user ever seeing it, so the rule is refused and the
// request stays pending for a one-off decision.
func TestAlwaysAllowRefusedForTruncatedDetail(t *testing.T) {
	srv := newTestServer(t)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "",
			strings.NewReader(paddedLabelBody("3000")))
	}()

	var pending *approval.Request
	deadline := time.Now().Add(2 * time.Second)
	for pending == nil && time.Now().Before(deadline) {
		if reqs := srv.approvalMgr.ListPending(); len(reqs) > 0 {
			pending = reqs[0]
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if pending == nil {
		t.Fatal("no approval request appeared")
	}
	if !approvalDetailTruncated(pending.Detail) {
		t.Fatalf("detail was not truncated, test is vacuous: %q", pending.Detail)
	}

	rr := srv.do(http.MethodPost, "/api/approval/always-allow", goodHost, testToken, "",
		strings.NewReader(`{"id":"`+pending.ID+`"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("always-allow status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if m := decodeBodyMap(t, rr); m["code"] != "approval_detail_truncated" {
		t.Errorf("code = %v, want approval_detail_truncated", m["code"])
	}
	if n := len(srv.approvalMgr.ListRules()); n != 0 {
		t.Errorf("rules = %d, want 0", n)
	}
	// Still pending: the user can decide this one request.
	if err := srv.approvalMgr.Respond(pending.ID, approval.Rejected); err != nil {
		t.Fatalf("request no longer pending after refused always-allow: %v", err)
	}
	if rr := <-done; rr.Code != http.StatusForbidden {
		t.Errorf("write status = %d, want 403 (rejected)", rr.Code)
	}
}

// A rule that already holds a truncated detail (persisted before such rules were
// refused) must not auto-approve a body that shares only the visible prefix.
func TestTruncatedDetailNeverAutoApproves(t *testing.T) {
	orig := approvalTimeout
	approvalTimeout = 10 * time.Millisecond
	t.Cleanup(func() { approvalTimeout = orig })

	srv := newTestServer(t)
	detail := "POST /ai-api/ports/label " + summarizeApprovalBody([]byte(paddedLabelBody("3000")))
	srv.approvalMgr.AddAlwaysAllowRule("api_write", detail)

	rr := srv.do(http.MethodPost, "/ai-api/ports/label", goodHost, "", "",
		strings.NewReader(paddedLabelBody("4000")))
	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408 (waited for approval, not auto-approved)", rr.Code)
	}
}

// A body past the preview cap must come out marked as truncated, even when the
// cut makes it non-JSON, so no always-allow rule can be built from it.
func TestCaptureBodyOversizedIsMarkedTruncated(t *testing.T) {
	s := &Server{}
	body := `{"label":"` + strings.Repeat("a", maxApprovalBodyBytes) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/ai-api/ports/label", strings.NewReader(body))
	got := s.captureBody(r, nil)
	if !approvalDetailTruncated(got) {
		t.Errorf("oversized body summary not marked truncated: %.80q", got)
	}
}
