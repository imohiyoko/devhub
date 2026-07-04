package server

import (
	"strings"
	"testing"
)

func TestSummarizeApprovalBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "  \n\t ", ""},
		{"non-secret preserved", `{"editor":"code --wait"}`, `{"editor":"code --wait"}`},
		{"redacts password", `{"host":"db","password":"hunter2"}`, `{"host":"db","password":"***"}`},
		{"redacts nested token", `{"conn":{"api_token":"abc","user":"me"}}`, `{"conn":{"api_token":"***","user":"me"}}`},
		{"redacts secret in array", `{"conns":[{"password":"x"}]}`, `{"conns":[{"password":"***"}]}`},
		{"normalizes key order and whitespace", `{ "b": 2, "a": 1 }`, `{"a":1,"b":2}`},
		{"non-json collapses whitespace", "not   json\nhere", "not json here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarizeApprovalBody([]byte(c.in)); got != c.want {
				t.Fatalf("summarizeApprovalBody(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSummarizeApprovalBodyTruncates(t *testing.T) {
	long := `{"v":"` + strings.Repeat("A", 1000) + `"}`
	got := summarizeApprovalBody([]byte(long))
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Fatalf("expected truncation suffix, got %q…", got[:min(len(got), 40)])
	}
	// The rune count before the suffix must not exceed the cap.
	head := strings.TrimSuffix(got, "…(truncated)")
	if n := len([]rune(head)); n != maxApprovalSummaryRunes {
		t.Fatalf("truncated head = %d runes, want %d", n, maxApprovalSummaryRunes)
	}
}
