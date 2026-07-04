package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	devhub "github.com/imohiyoko/devhub"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	st, err := Open(home, devhub.Assets)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, home
}

func TestSettingsSeedAndRoundTrip(t *testing.T) {
	st, _ := openTest(t)
	s, err := st.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if s["port"] == nil {
		t.Error("expected port seeded from example")
	}
	if err := st.SaveSettings(map[string]any{"editor": "cursor"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	s2, _ := st.LoadSettings()
	if s2["editor"] != "cursor" {
		t.Errorf("editor = %v, want cursor", s2["editor"])
	}
	// Saving a patch must not wipe other keys.
	if s2["port"] == nil {
		t.Error("port lost after patch save")
	}
}

func TestConfigSeededFromExample(t *testing.T) {
	st, _ := openTest(t)
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, ok := cfg["scan_roots"]; !ok {
		t.Errorf("config missing scan_roots: %v", cfg)
	}
}

// TestKVSeamRoundTrip covers the raw core.Store seam (Get/Set) that *Store now
// satisfies, including the absent-key nil contract callers rely on.
func TestKVSeamRoundTrip(t *testing.T) {
	st, _ := openTest(t)
	if b, err := st.Get("tool:git"); err != nil || b != nil {
		t.Fatalf("Get(absent) = %v, %v; want nil, nil", b, err)
	}
	if err := st.Set("tool:git", []byte(`{"foo":"bar"}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := st.Get("tool:git")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != `{"foo":"bar"}` {
		t.Errorf("Get = %q, want the bytes written verbatim", got)
	}
	// Set must overwrite, not append.
	if err := st.Set("tool:git", []byte(`{"foo":"baz"}`)); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	got, _ = st.Get("tool:git")
	if string(got) != `{"foo":"baz"}` {
		t.Errorf("after overwrite Get = %q", got)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	st, _ := openTest(t)
	// Open() already ran migrate once and set meta.migrated. A second call is a no-op.
	if err := st.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var v string
	if err := st.db.QueryRow("SELECT value FROM meta WHERE key='migrated'").Scan(&v); err != nil {
		t.Fatalf("migrated flag missing: %v", err)
	}
	if v != "1" {
		t.Errorf("migrated = %q, want 1", v)
	}
}

func TestMigrationImportsLegacyJSON(t *testing.T) {
	home := t.TempDir()
	sd := filepath.Join(home, "settings")
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed a legacy server.json before first Open.
	legacy := map[string]any{"editor": "windsurf", "port": 9000}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(sd, "server.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(home, devhub.Assets)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	s, _ := st.LoadSettings()
	if s["editor"] != "windsurf" {
		t.Errorf("legacy server.json not migrated: editor=%v", s["editor"])
	}
}

func TestWALFilesCreated(t *testing.T) {
	_, home := openTest(t)
	walPath := filepath.Join(home, "settings", "devhub.db-wal")
	if _, err := os.Stat(walPath); err != nil {
		t.Errorf("expected WAL sidecar at %s: %v", walPath, err)
	}
}

func TestLaunchesRoundTrip(t *testing.T) {
	st, _ := openTest(t)
	in := map[string]any{"launches": []any{
		map[string]any{"launch_id": "a", "launched_at": "2026-01-01T00:00:00Z", "env_id": "x"},
	}}
	if err := st.SaveLaunches(in); err != nil {
		t.Fatalf("SaveLaunches: %v", err)
	}
	out, err := st.LoadLaunches()
	if err != nil {
		t.Fatalf("LoadLaunches: %v", err)
	}
	list, _ := out["launches"].([]any)
	if len(list) != 1 {
		t.Fatalf("launches len = %d, want 1", len(list))
	}
	rec := list[0].(map[string]any)
	if rec["launch_id"] != "a" {
		t.Errorf("launch_id = %v, want a", rec["launch_id"])
	}
}
