package storage

import "testing"

// AppendLaunch / RemoveLaunch are single-row operations (the cross-process
// safety `devhub env start` relies on): appending must not disturb existing
// rows, removing must delete exactly the target, and an id-less record is
// rejected instead of silently colliding on the empty primary key.
func TestAppendRemoveLaunchRowLevel(t *testing.T) {
	st, _ := openTest(t)

	a := map[string]any{"launch_id": "a", "env_id": "e1", "launched_at": "2026-07-05T00:00:00Z"}
	b := map[string]any{"launch_id": "b", "env_id": "e2", "launched_at": "2026-07-05T00:00:01Z"}
	if err := st.AppendLaunch(a); err != nil {
		t.Fatalf("AppendLaunch(a): %v", err)
	}
	if err := st.AppendLaunch(b); err != nil {
		t.Fatalf("AppendLaunch(b): %v", err)
	}

	data, err := st.LoadLaunches()
	if err != nil {
		t.Fatalf("LoadLaunches: %v", err)
	}
	list, _ := data["launches"].([]any)
	if len(list) != 2 {
		t.Fatalf("after two appends got %d records", len(list))
	}

	if err := st.RemoveLaunch("a"); err != nil {
		t.Fatalf("RemoveLaunch(a): %v", err)
	}
	data, _ = st.LoadLaunches()
	list, _ = data["launches"].([]any)
	if len(list) != 1 {
		t.Fatalf("after remove got %d records", len(list))
	}
	rec, _ := list[0].(map[string]any)
	if id, _ := rec["launch_id"].(string); id != "b" {
		t.Fatalf("surviving record = %q, want b", id)
	}

	// Removing a missing id is a no-op (caller reports its own error).
	if err := st.RemoveLaunch("ghost"); err != nil {
		t.Fatalf("RemoveLaunch(ghost): %v", err)
	}

	if err := st.AppendLaunch(map[string]any{"env_id": "no-id"}); err == nil {
		t.Fatal("AppendLaunch without launch_id should error")
	}
}
