package server

import (
	"net/http"
	"strings"
	"testing"
)

// The docs routes exist for callers that arrived over /ai-api and need to read
// their way out of a failure. They are GETs, so they must answer immediately —
// an agent that had to wait for a human to approve reading the manual could not
// use the manual to find out how approval works.
func TestDocsReadableOverAiAPIWithoutApproval(t *testing.T) {
	srv := newTestServer(t)

	rr := srv.do(http.MethodGet, "/ai-api/docs", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "agent/troubleshooting") {
		t.Errorf("list does not mention agent/troubleshooting: %s", body)
	}

	rr = srv.do(http.MethodGet, "/ai-api/docs/agent/troubleshooting", goodHost, "", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("show status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	m := decodeBodyMap(t, rr)
	if m["name"] != "agent/troubleshooting" {
		t.Errorf("name = %v", m["name"])
	}
	if body, _ := m["body"].(string); !strings.Contains(body, "approval_timeout") {
		t.Error("troubleshooting doc no longer documents approval_timeout")
	}
}

// The hints emitted by the router name codes that the troubleshooting doc is
// supposed to explain. If a code is added without a matching section, an agent
// following the hint arrives at a page that does not mention its problem.
func TestTroubleshootingDocCoversEveryEmittedCode(t *testing.T) {
	srv := newTestServer(t)
	rr := srv.do(http.MethodGet, "/ai-api/docs/agent/troubleshooting", goodHost, "", "", nil)
	doc, _ := decodeBodyMap(t, rr)["body"].(string)

	for _, code := range []string{
		"missing_token", "not_loopback", "cross_site", "host_not_allowed",
		"approval_rejected", "approval_timeout", "no_ai_api_route",
	} {
		if !strings.Contains(doc, code) {
			t.Errorf("docs/agent/troubleshooting.md does not document %q", code)
		}
	}
}

func TestUnknownDocIs404WithCode(t *testing.T) {
	srv := newTestServer(t)
	rr := srv.do(http.MethodGet, "/ai-api/docs/no/such/thing", goodHost, "", "", nil)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if m := decodeBodyMap(t, rr); m["code"] != "doc_not_found" {
		t.Errorf("code = %v, want doc_not_found", m["code"])
	}
}
