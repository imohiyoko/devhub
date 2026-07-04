package ports

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// fakeSettingsStore is a map-based, SQLite-free stand-in for the shared settings
// document. It mirrors the real store's per-key JSON round-trip (each patch key
// is stored as encoded JSON), so values come back through LoadSettings the same
// shape a real save->load would produce — a []int protected list, for instance,
// returns as []any of float64. It satisfies the ports settingsStore interface.
type fakeSettingsStore struct {
	docs map[string]json.RawMessage
}

func newFakeSettingsStore() *fakeSettingsStore {
	return &fakeSettingsStore{docs: map[string]json.RawMessage{}}
}

func (f *fakeSettingsStore) LoadSettings() (map[string]any, error) {
	out := map[string]any{}
	for k, raw := range f.docs {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

func (f *fakeSettingsStore) SaveSettings(patch map[string]any) error {
	for k, v := range patch {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		f.docs[k] = b
	}
	return nil
}

// TestPortsHandlersWithFakeStore drives the label and protected-ports POST
// handlers against an in-memory fake — no SQLite — proving the ports controller
// depends only on its narrow settingsStore interface. Both endpoints skip the
// OS port scan, so the whole test is hermetic.
func TestPortsHandlersWithFakeStore(t *testing.T) {
	c := New(newFakeSettingsStore())

	// POST /api/ports/label persists a label, readable back via LoadSettings.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/ports/label", nil)
	if err := c.HandlePost(rec, req, map[string]any{"port": float64(8080), "label": "web"}); err != nil {
		t.Fatalf("label HandlePost: %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("label status = %d, want 200", rec.Code)
	}
	if got := c.portLabels()["8080"]; got != "web" {
		t.Errorf("port_labels[8080] = %q, want web", got)
	}

	// Clearing the label (empty string) removes it.
	rec = httptest.NewRecorder()
	if err := c.HandlePost(rec, req, map[string]any{"port": float64(8080), "label": ""}); err != nil {
		t.Fatalf("clear-label HandlePost: %v", err)
	}
	if _, ok := c.portLabels()["8080"]; ok {
		t.Error("port_labels[8080] should be removed after an empty label")
	}

	// POST /api/ports/protected normalizes (dedupes, sorts, coerces strings) and
	// persists the list; protectedPorts() reads it back through the same fake.
	rec = httptest.NewRecorder()
	preq := httptest.NewRequest("POST", "/api/ports/protected", nil)
	if err := c.HandlePost(rec, preq, map[string]any{"ports": []any{float64(3001), "3000", float64(3000)}}); err != nil {
		t.Fatalf("protected HandlePost: %v", err)
	}
	if got := c.protectedPorts(); len(got) != 2 || got[0] != 3000 || got[1] != 3001 {
		t.Errorf("protectedPorts = %v, want [3000 3001]", got)
	}
}

func TestParseLsof(t *testing.T) {
	// Header + a normal row, an escaped-space command, an IPv6 row, and a junk row.
	out := "COMMAND   PID  USER   FD   TYPE   DEVICE SIZE/OFF NODE NAME\n" +
		"node    16727  alice  23u  IPv4 0x1234      0t0  TCP *:3000 (LISTEN)\n" +
		"Google\\x20Chrome 999 alice 50u IPv4 0xabcd 0t0 TCP 127.0.0.1:8080 (LISTEN)\n" +
		"sshd      55  root   3u   IPv6 0x9999      0t0  TCP [::1]:22 (LISTEN)\n" +
		"garbage line without listen\n"
	got := parseLsof(out)
	if len(got) != 3 {
		t.Fatalf("parseLsof returned %d rows, want 3: %+v", len(got), got)
	}
	if got[0].command != "node" || got[0].pid != 16727 || got[0].port != 3000 || got[0].host != "*" {
		t.Errorf("row0 = %+v", got[0])
	}
	if got[1].command != "Google Chrome" || got[1].port != 8080 || got[1].host != "127.0.0.1" {
		t.Errorf("row1 (escaped space / host) = %+v", got[1])
	}
	if got[2].host != "[::1]" || got[2].port != 22 {
		t.Errorf("row2 (ipv6) = %+v", got[2])
	}
}

func TestParseNetstat(t *testing.T) {
	out := "\nActive Connections\n\n  Proto  Local Address          Foreign Address        State           PID\n" +
		"  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1044\n" +
		"  TCP    127.0.0.1:5354         0.0.0.0:0              ESTABLISHED     2222\n" +
		"  TCP    [::]:445               [::]:0                 LISTENING       4\n"
	got := parseNetstat(out)
	if len(got) != 2 {
		t.Fatalf("parseNetstat returned %d rows, want 2: %+v", len(got), got)
	}
	if got[0].port != 135 || got[0].pid != 1044 || got[0].host != "0.0.0.0" {
		t.Errorf("row0 = %+v", got[0])
	}
	if got[1].port != 445 || got[1].pid != 4 || got[1].host != "[::]" {
		t.Errorf("row1 (ipv6) = %+v", got[1])
	}
}
