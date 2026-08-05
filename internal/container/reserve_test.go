package container

import (
	"strings"
	"testing"
)

const hostMem32GiB = 32 * gibDivisor

// TestCapsOnThisShapeOfHost pins the arithmetic against a 10-core / 32 GiB Mac,
// which is the machine this was written on and the one the numbers in the plan
// refer to.
func TestCapsOnThisShapeOfHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reserve Reserve
		wantCPU int
		wantMem int
	}{
		// The default: a fifth held back. 20% of 10 cores is 2, and 20% of
		// 32 GiB is 6.4 — the memory cap is 25 because the subtraction happens
		// in bytes and only the answer is floored to GiB.
		{"default", DefaultReserve(), 8, 25},
		{"nothing reserved",
			Reserve{CPU: Amount{Percent: 0, Set: true}, Memory: Amount{Percent: 0, Set: true}}, 10, 32},
		{"absolute",
			Reserve{CPU: Amount{Absolute: 4, Set: true}, Memory: Amount{Absolute: 8, Set: true}}, 6, 24},
		{"half",
			Reserve{CPU: Amount{Percent: 50, Set: true}, Memory: Amount{Percent: 50, Set: true}}, 5, 16},
		// An unset half falls back to the default rather than to zero, so a
		// settings document naming only one resource is still coherent.
		{"only cpu configured",
			Reserve{CPU: Amount{Absolute: 1, Set: true}}, 9, 25},
	} {
		if got := tc.reserve.CPUCap(10); got != tc.wantCPU {
			t.Errorf("%s: CPUCap = %d, want %d", tc.name, got, tc.wantCPU)
		}
		if got := tc.reserve.MemoryCapGiB(hostMem32GiB); got != tc.wantMem {
			t.Errorf("%s: MemoryCapGiB = %d, want %d", tc.name, got, tc.wantMem)
		}
	}
}

// TestCapsNeverRefuseEverything. A reserve big enough to leave nothing would
// make the cap zero, and a zero cap refuses every size a caller could name —
// including one core on a machine that plainly has one to give. Refusing
// everything is the one answer that cannot be what the user meant, so the floor
// is one rather than the arithmetic result.
func TestCapsNeverRefuseEverything(t *testing.T) {
	r := Reserve{CPU: Amount{Percent: 90, Set: true}, Memory: Amount{Percent: 90, Set: true}}
	if got := r.CPUCap(2); got != 1 {
		t.Errorf("CPUCap = %d, want 1", got)
	}
	if got := r.MemoryCapGiB(4 * gibDivisor); got != 1 {
		t.Errorf("MemoryCapGiB = %d, want 1", got)
	}
	// Absolutes larger than the host are the other way in.
	huge := Reserve{CPU: Amount{Absolute: 999, Set: true}, Memory: Amount{Absolute: 999, Set: true}}
	if got := huge.CPUCap(10); got != 1 {
		t.Errorf("CPUCap = %d, want 1", got)
	}
	if got := huge.MemoryCapGiB(hostMem32GiB); got != 1 {
		t.Errorf("MemoryCapGiB = %d, want 1", got)
	}
}

// TestCapsSayZeroWhenTheHostIsUnknown: a cap of zero is the signal that there
// is nothing to compare against, and callers must fall back rather than refuse.
func TestCapsSayZeroWhenTheHostIsUnknown(t *testing.T) {
	r := DefaultReserve()
	if got := r.CPUCap(0); got != 0 {
		t.Errorf("CPUCap(0) = %d, want 0", got)
	}
	if got := r.MemoryCapGiB(0); got != 0 {
		t.Errorf("MemoryCapGiB(0) = %d, want 0", got)
	}
}

func TestNormalizeReserveAccepts(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want Reserve
	}{
		{"absent", nil, DefaultReserve()},
		{"percent both", map[string]any{
			"cpu":    map[string]any{"percent": float64(25)},
			"memory": map[string]any{"percent": float64(10)},
		}, Reserve{CPU: Amount{Percent: 25, Set: true}, Memory: Amount{Percent: 10, Set: true}}},
		// The two forms mix freely: they answer different questions, so there
		// is no reason to make a user pick one style for the whole document.
		{"mixed forms", map[string]any{
			"cpu":    map[string]any{"cores": float64(2)},
			"memory": map[string]any{"percent": float64(20)},
		}, Reserve{CPU: Amount{Absolute: 2, Set: true}, Memory: Amount{Percent: 20, Set: true}}},
		{"only one resource", map[string]any{
			"cpu": map[string]any{"cores": float64(1)},
		}, Reserve{CPU: Amount{Absolute: 1, Set: true}, Memory: Amount{Percent: 20, Set: true}}},
		// An explicit zero survives the defaulting pass; that is what Set is
		// for.
		{"explicit zero", map[string]any{
			"cpu":    map[string]any{"percent": float64(0)},
			"memory": map[string]any{"gib": float64(0)},
		}, Reserve{CPU: Amount{Percent: 0, Set: true}, Memory: Amount{Absolute: 0, Set: true}}},
	} {
		got, err := NormalizeReserve(tc.in)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeReserveRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string // a fragment the message must carry
	}{
		{"not an object", "20%", "オブジェクト"},
		// Both forms at once: whichever devhub picked, the other would sit in
		// the settings document looking like it was in effect.
		{"both forms", map[string]any{
			"cpu": map[string]any{"percent": float64(20), "cores": float64(2)},
		}, "どちらか一方"},
		{"neither form", map[string]any{"cpu": map[string]any{}}, "が要ります"},
		{"percent too high", map[string]any{
			"cpu": map[string]any{"percent": float64(95)},
		}, "0〜90"},
		{"negative percent", map[string]any{
			"cpu": map[string]any{"percent": float64(-5)},
		}, "0〜90"},
		{"negative absolute", map[string]any{
			"memory": map[string]any{"gib": float64(-1)},
		}, "0 以上"},
		{"fractional", map[string]any{
			"cpu": map[string]any{"cores": 1.5},
		}, "整数"},
		{"not a number", map[string]any{
			"cpu": map[string]any{"percent": "20"},
		}, "数値"},
		// A misspelled key must not leave the default quietly in force while
		// the settings screen shows the value the user thought they set.
		{"unknown key", map[string]any{
			"cpu": map[string]any{"percentage": float64(20)},
		}, "未知のキー"},
		{"unknown resource", map[string]any{
			"disk": map[string]any{"percent": float64(20)},
		}, "未知のキー"},
		// The unit belongs to the resource: GiB is not a count of cores.
		{"wrong unit for cpu", map[string]any{
			"cpu": map[string]any{"gib": float64(2)},
		}, "未知のキー"},
	} {
		got, err := NormalizeReserve(tc.in)
		if err == nil {
			t.Errorf("%s: accepted, got %+v", tc.name, got)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: message = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// TestReserveJSONRoundTrips: what the settings endpoint stores must parse back
// to the same policy, or the screen and the caps drift apart.
func TestReserveJSONRoundTrips(t *testing.T) {
	for _, want := range []Reserve{
		DefaultReserve(),
		{CPU: Amount{Absolute: 3, Set: true}, Memory: Amount{Absolute: 8, Set: true}},
		{CPU: Amount{Percent: 50, Set: true}, Memory: Amount{Absolute: 0, Set: true}},
	} {
		encoded := want.JSON()
		// The map has to survive a trip through JSON, where every number
		// becomes a float64 — the same shape a request arrives in.
		asRequest := map[string]any{}
		for k, v := range encoded {
			inner := map[string]any{}
			for ik, iv := range v.(map[string]any) {
				inner[ik] = float64(iv.(int))
			}
			asRequest[k] = inner
		}
		got, err := NormalizeReserve(asRequest)
		if err != nil {
			t.Errorf("%+v: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("round trip: got %+v, want %+v (via %v)", got, want, encoded)
		}
	}
}
