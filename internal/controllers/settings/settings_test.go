package settings

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"

	devhub "github.com/imohiyoko/devhub"
	"github.com/imohiyoko/devhub/internal/core"
	"github.com/imohiyoko/devhub/internal/storage"
)

// TestToolSettingsSeam exercises the per-tool settings path end-to-end through
// the core.Store seam: POST persists under key "tool:<id>", GET reads it back,
// and the stored bytes are the caller's JSON verbatim (no HTML escaping).
func TestToolSettingsSeam(t *testing.T) {
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	c := New(st, core.Namespace(st, "tool"), 8765)

	// A value containing '&' would be mangled by HTML-escaping marshalers.
	in := map[string]any{"endpoint": "https://x/y?a=1&b=2"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/settings/tool/git", nil)
	if err := c.HandlePost(rec, req, in); err != nil {
		t.Fatalf("HandlePost: %v", err)
	}

	// Stored under the namespaced key, byte-for-byte.
	raw, err := st.Get("tool:git")
	if err != nil {
		t.Fatalf("Get(tool:git): %v", err)
	}
	if string(raw) != `{"endpoint":"https://x/y?a=1&b=2"}` {
		t.Errorf("stored bytes = %q, want the JSON unescaped", raw)
	}

	// GET reads it back through the same seam.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/settings/tool/git", nil)
	if err := c.HandleGet(rec, req); err != nil {
		t.Fatalf("HandleGet: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if out["endpoint"] != "https://x/y?a=1&b=2" {
		t.Errorf("round-trip endpoint = %v", out["endpoint"])
	}

	// An unknown tool returns an empty object, not an error.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/settings/tool/absent", nil)
	if err := c.HandleGet(rec, req); err != nil {
		t.Fatalf("HandleGet(absent): %v", err)
	}
	if got := rec.Body.String(); got != "{}\n" && got != "{}" {
		t.Errorf("absent tool GET = %q, want empty object", got)
	}
}

// TestVMReserveIsValidatedAtSaveTime. The value only matters later, when a
// profile size is judged — so a malformed one that was accepted here would be a
// saved setting the screen shows and the machine quietly ignores. It is stored
// canonically for the same reason: the reader must not have to guess which of
// two spellings was meant.
func TestVMReserveIsValidatedAtSaveTime(t *testing.T) {
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := New(st, core.Namespace(st, "tool"), 8765)

	save := func(v any) error {
		return c.HandlePost(httptest.NewRecorder(),
			httptest.NewRequest("POST", "/api/settings", nil),
			map[string]any{"vm_reserve": v})
	}

	if err := save(map[string]any{
		"cpu":    map[string]any{"percent": float64(25)},
		"memory": map[string]any{"gib": float64(8)},
	}); err != nil {
		t.Fatalf("a valid reserve was refused: %v", err)
	}
	settings, err := st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(settings["vm_reserve"])
	if want := `{"cpu":{"percent":25},"memory":{"gib":8}}`; string(got) != want {
		t.Errorf("stored = %s\nwant     %s", got, want)
	}

	for _, tc := range []struct {
		name string
		in   any
	}{
		{"both forms at once", map[string]any{
			"cpu": map[string]any{"percent": float64(20), "cores": float64(2)}}},
		{"percent out of range", map[string]any{
			"cpu": map[string]any{"percent": float64(99)}}},
		{"misspelled key", map[string]any{
			"memory": map[string]any{"gigabytes": float64(8)}}},
		{"not an object", "20%"},
	} {
		if err := save(tc.in); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}

	// A refused save must leave the previous value alone — otherwise a typo
	// silently loosens the cap.
	settings, err = st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	got, _ = json.Marshal(settings["vm_reserve"])
	if want := `{"cpu":{"percent":25},"memory":{"gib":8}}`; string(got) != want {
		t.Errorf("a rejected save changed the stored value: %s", got)
	}
}

func TestServerSettingsAreValidatedAndPersisted(t *testing.T) {
	st, err := storage.Open(t.TempDir(), devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := New(st, core.Namespace(st, "tool"), 4321)
	save := func(data map[string]any) error {
		return c.HandlePost(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/settings", nil), data)
	}
	if err := save(map[string]any{"port": float64(4321), "db_local_only": false}); err != nil {
		t.Fatalf("valid settings refused: %v", err)
	}
	got, err := st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got["port"] != float64(4321) && got["port"] != 4321 {
		t.Fatalf("port = %#v", got["port"])
	}
	if got["db_local_only"] != false {
		t.Fatalf("db_local_only = %#v", got["db_local_only"])
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := save(map[string]any{"port": float64(occupied.Addr().(*net.TCPAddr).Port)}); err == nil {
		t.Fatal("occupied port was accepted")
	}

	for _, data := range []map[string]any{
		{"port": float64(80)}, {"port": float64(65536)}, {"port": 1.5}, {"port": "8765"},
		{"db_local_only": "false"},
	} {
		if err := save(data); err == nil {
			t.Errorf("invalid settings accepted: %#v", data)
		}
	}
	got, err = st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got["port"] != float64(4321) && got["port"] != 4321 {
		t.Fatalf("rejected save changed port: %#v", got["port"])
	}
	if got["db_local_only"] != false {
		t.Fatalf("rejected save changed db_local_only: %#v", got["db_local_only"])
	}
}
