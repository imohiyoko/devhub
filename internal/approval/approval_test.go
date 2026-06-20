package approval

import (
	"testing"
	"time"
)

func TestApprovalFlow_Approve(t *testing.T) {
	mgr := NewManager(nil)
	req := mgr.Register("test_action", "test_detail")

	if req.Status != Pending {
		t.Fatalf("expected status to be pending, got %v", req.Status)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		err := mgr.Respond(req.ID, Approved)
		if err != nil {
			t.Errorf("respond failed: %v", err)
		}
	}()

	decision, err := mgr.Wait(req, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	if decision != Approved {
		t.Errorf("expected decision to be approved, got %v", decision)
	}
}

func TestApprovalFlow_Reject(t *testing.T) {
	mgr := NewManager(nil)
	req := mgr.Register("test_action", "test_detail")

	go func() {
		time.Sleep(50 * time.Millisecond)
		err := mgr.Respond(req.ID, Rejected)
		if err != nil {
			t.Errorf("respond failed: %v", err)
		}
	}()

	decision, err := mgr.Wait(req, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}

	if decision != Rejected {
		t.Errorf("expected decision to be rejected, got %v", decision)
	}
}

func TestApprovalFlow_Timeout(t *testing.T) {
	mgr := NewManager(nil)
	req := mgr.Register("test_action", "test_detail")

	decision, err := mgr.Wait(req, 20*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}

	if decision != Rejected {
		t.Errorf("expected decision on timeout to be rejected, got %v", decision)
	}

	// Verify status updated in manager
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	if req.Status != Rejected {
		t.Errorf("expected status to be rejected after timeout, got %v", req.Status)
	}
}

func TestAlwaysAllowRules(t *testing.T) {
	mgr := NewManager(nil)
	action := "execute"
	detail := "git commit -m 'test'"

	if mgr.ShouldAutoApprove(action, detail) {
		t.Fatalf("expected false before rule addition")
	}

	id := mgr.AddAlwaysAllowRule(action, "git commit")
	if !mgr.ShouldAutoApprove(action, detail) {
		t.Fatalf("expected true after rule addition")
	}

	// Other detail shouldn't match
	if mgr.ShouldAutoApprove(action, "git push") {
		t.Fatalf("expected false for mismatching detail prefix")
	}

	rules := mgr.ListRules()
	if len(rules) != 1 || rules[0].ID != id {
		t.Fatalf("expected 1 rule with id %s, got %v", id, rules)
	}

	err := mgr.DeleteRule(id)
	if err != nil {
		t.Fatalf("delete rule failed: %v", err)
	}

	if mgr.ShouldAutoApprove(action, detail) {
		t.Fatalf("expected false after rule deletion")
	}
}
