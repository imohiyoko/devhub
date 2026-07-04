package settings

import (
	"encoding/json"
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

	c := New(st, core.Namespace(st, "tool"))

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
