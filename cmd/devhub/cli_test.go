package main

import "testing"

// TestRunSubcommandDispatch pins the non-blocking dispatch outcomes: help and
// version succeed, and an unknown word errors (exit 2) instead of falling
// through to a server start — the core guarantee of ADR 0002 decision 5 and
// 0003 (a reflexive/typo'd `devhub` never launches a server). `start` is
// intentionally excluded: it blocks in srv.Run.
func TestRunSubcommandDispatch(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"help", 0},
		{"version", 0},
		{"stpo", 2}, // typo of `stop` must not start a server
	}
	for _, c := range cases {
		if got := runSubcommand(c.name, nil); got != c.want {
			t.Errorf("runSubcommand(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}
