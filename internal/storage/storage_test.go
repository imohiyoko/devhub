package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// TestEnvsSeedAndRoundTrip pins the v1 env-launcher config path: the embedded
// example seeds first-run state unchanged, and a load->save->load through the
// SQLite kv row is meaning-preserving. This is the backstop for the planned
// dual-codec work (typed model in memory, v1 shape at rest).
func TestEnvsSeedAndRoundTrip(t *testing.T) {
	st, _ := openTest(t)
	envs1, err := st.LoadEnvs()
	if err != nil {
		t.Fatalf("LoadEnvs: %v", err)
	}
	seeded, ok := envs1["environments"].([]any)
	if !ok || len(seeded) == 0 {
		t.Fatalf("expected environments seeded from envs.example.json, got %v", envs1)
	}
	if err := st.SaveEnvs(envs1); err != nil {
		t.Fatalf("SaveEnvs: %v", err)
	}
	envs2, err := st.LoadEnvs()
	if err != nil {
		t.Fatalf("LoadEnvs after save: %v", err)
	}
	if !reflect.DeepEqual(envs1, envs2) {
		t.Errorf("envs changed across save round-trip:\nbefore: %#v\nafter:  %#v", envs1, envs2)
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

func TestSettingsOwnershipRouting(t *testing.T) {
	st, _ := openTest(t)
	patch := map[string]any{"editor": "zed", "protected_ports": []any{3000.0}}
	if err := st.SaveSettings(patch); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	// Each key lands in its owner's row: settings.editor / ports.protected_ports.
	var v string
	if err := st.db.QueryRow("SELECT value FROM kv WHERE key = 'settings.editor'").Scan(&v); err != nil {
		t.Errorf("settings.editor row missing: %v", err)
	}
	if err := st.db.QueryRow("SELECT value FROM kv WHERE key = 'ports.protected_ports'").Scan(&v); err != nil {
		t.Errorf("ports.protected_ports row missing: %v", err)
	}
	// And no whole-document blob is written anymore.
	if raw, _ := st.kvGet("settings"); raw != nil {
		t.Errorf("legacy settings blob written by SaveSettings: %s", raw)
	}
	// Both read back through the flat-map view.
	s, err := st.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if s["editor"] != "zed" {
		t.Errorf("editor = %v, want zed", s["editor"])
	}
	if pp, ok := s["protected_ports"].([]any); !ok || len(pp) != 1 {
		t.Errorf("protected_ports = %v, want one entry", s["protected_ports"])
	}
}

func TestSettingsPatchWritesOnlyItsRows(t *testing.T) {
	st, _ := openTest(t)
	if err := st.SaveSettings(map[string]any{"editor": "cursor"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	var n int
	if err := st.db.QueryRow(
		"SELECT COUNT(*) FROM kv WHERE key LIKE 'settings.%' OR key LIKE 'ports.%'",
	).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Errorf("patch of one key wrote %d rows, want 1", n)
	}
}

func TestSettingsBlobMigratedToRows(t *testing.T) {
	st, _ := openTest(t)
	// Simulate a pre-rows database: put the legacy blob back.
	blob := map[string]any{"editor": "vim", "protected_ports": []any{5432.0}}
	if err := kvSet(st.db, "settings", blob); err != nil {
		t.Fatalf("seed blob: %v", err)
	}
	if err := st.migrateSettingsRows(); err != nil {
		t.Fatalf("migrateSettingsRows: %v", err)
	}
	if raw, _ := st.kvGet("settings"); raw != nil {
		t.Errorf("legacy blob still present after migration: %s", raw)
	}
	s, _ := st.LoadSettings()
	if s["editor"] != "vim" {
		t.Errorf("editor = %v, want vim (from migrated blob)", s["editor"])
	}
	if pp, ok := s["protected_ports"].([]any); !ok || len(pp) != 1 {
		t.Errorf("protected_ports = %v, want one entry", s["protected_ports"])
	}
	// Second run is a no-op.
	if err := st.migrateSettingsRows(); err != nil {
		t.Errorf("second migrateSettingsRows: %v", err)
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

// TestSaveLaunchesSkipsEmptyID: records with an empty launch_id are dropped
// rather than colliding on the primary key. Two id-less records must not
// silently overwrite each other, and a valid record alongside them survives.
func TestSaveLaunchesSkipsEmptyID(t *testing.T) {
	st, _ := openTest(t)
	in := map[string]any{"launches": []any{
		map[string]any{"launch_id": "", "env_id": "first"},
		map[string]any{"launch_id": "", "env_id": "second"},
		map[string]any{"launch_id": "keep", "env_id": "ok"},
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
		t.Fatalf("launches len = %d, want 1 (both empty ids skipped)", len(list))
	}
	if id := list[0].(map[string]any)["launch_id"]; id != "keep" {
		t.Errorf("surviving launch_id = %v, want keep", id)
	}
}

// TestMigrationWarnings: a clean Open reports no warnings, and the accessor
// reflects recorded warnings (the plumbing /api/info exposes to the dashboard).
func TestMigrationWarnings(t *testing.T) {
	st, _ := openTest(t)
	if w := st.MigrationWarnings(); len(w) != 0 {
		t.Errorf("clean Open should have no migration warnings, got %v", w)
	}
	st.recordMigrationWarning("simulated failure", errors.New("boom"))
	w := st.MigrationWarnings()
	if len(w) != 1 || w[0] != "simulated failure: boom" {
		t.Errorf("MigrationWarnings = %v, want one formatted message", w)
	}
}
