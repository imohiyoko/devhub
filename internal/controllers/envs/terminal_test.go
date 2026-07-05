package envs

import "testing"

// The composed "set env ; run" command must reach Windows Terminal with ';'
// escaped, so wt forwards it to a single shell instead of splitting the launch
// across tabs (which left the real command malformed and failing with
// 0x80070002). A ';'-free command must pass through unchanged.
func TestWtEscape(t *testing.T) {
	got := wtEscape("$env:DEVHUB_PORT='8766' ; go run ./cmd/devhub start")
	want := `$env:DEVHUB_PORT='8766' \; go run ./cmd/devhub start`
	if got != want {
		t.Errorf("wtEscape() = %q, want %q", got, want)
	}
	if got := wtEscape("go run ./cmd/devhub start"); got != "go run ./cmd/devhub start" {
		t.Errorf("wtEscape() altered a ';'-free command: %q", got)
	}
}
