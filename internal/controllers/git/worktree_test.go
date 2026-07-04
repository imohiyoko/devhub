package git

import "testing"

func TestWorktreePathKnown(t *testing.T) {
	worktrees := []map[string]any{
		{"path": "/repo/main"},
		{"path": "/repo/wt-feature"},
	}
	if !worktreePathKnown(worktrees, "/repo/wt-feature") {
		t.Error("a registered worktree path should be known")
	}
	// A well-formed path that is not one of the repo's worktrees must be rejected,
	// so pull/push cannot run git in an arbitrary directory.
	if worktreePathKnown(worktrees, "/repo/other") {
		t.Error("an unregistered path must not be known")
	}
	if worktreePathKnown(nil, "/repo/main") {
		t.Error("an empty worktree set matches nothing")
	}
}
