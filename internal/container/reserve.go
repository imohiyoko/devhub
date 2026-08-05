package container

// How much of the host devhub keeps away from a VM, and the caps that follow
// from it.
//
// The host's own numbers are facts (internal/hostspec); this is the policy laid
// over them, and it is the only part a user sets. That split is the point: the
// machine's cores and memory are not something anyone should have to type in,
// and a number the user maintains by hand is a number that goes stale the day
// they change machines.
//
// Two ways to express it, because they answer different questions. A percentage
// travels — "always leave macOS a fifth" stays right on the next Mac. An
// absolute is what someone says when they know the actual figure they need free
// ("keep 8 GiB for the browser"). Neither is a special case of the other, so
// both are accepted rather than one being converted into the other at save
// time, which would print back something the user did not write.

import (
	"fmt"
	"sort"
	"strings"
)

// gibDivisor is one GiB, in bytes. Sizes reach colima in GiB, so the byte
// figures the host reports are floored to that unit before anything compares
// them — a cap of 25.6 GiB permits 25, and saying so in the units of the field
// the user is typing into is the only way the refusal reads as arithmetic
// rather than as an off-by-one.
const gibDivisor = 1 << 30

// maxReservePercent bounds a percentage reserve. Above this the cap is more
// floor than cap and every realistic size is refused; a config that refuses
// everything is a mistake devhub can recognise, so it says so at save time
// rather than at the first create.
const maxReservePercent = 90

// Amount is one resource's reserve. At most one of Percent and Absolute is
// non-zero: Percent for a fraction of what the host has, Absolute for a fixed
// number of cores (CPU) or GiB (memory).
//
// Set is what separates "reserve nothing" from "not configured", and it earns
// its keep: both are all-zero numbers, and only one of them should be replaced
// by the default. Without it, a user who deliberately gave the VM the whole
// machine would silently get 20% held back — and there would be nothing on
// screen explaining where the missing cores went. It also makes a partially
// written setting safe: {"cpu": {...}} alone leaves memory on the default.
type Amount struct {
	Percent  int
	Absolute int
	Set      bool
}

func (a Amount) unset() bool { return !a.Set }

// Reserve is the policy for both resources devhub caps. Disk is absent on
// purpose: Lima's disk images are sparse, so a profile declaring more disk than
// the volume has free is not a mistake, and there is nothing to hold back.
type Reserve struct {
	CPU    Amount
	Memory Amount
}

// DefaultReserve is what an unconfigured devhub uses: a fifth of the machine
// left to the machine. It is a default rather than a hard floor — someone who
// wants the VM to have everything can say so.
func DefaultReserve() Reserve {
	return Reserve{
		CPU:    Amount{Percent: 20, Set: true},
		Memory: Amount{Percent: 20, Set: true},
	}
}

// withDefaults fills in whichever half was not configured.
func (r Reserve) withDefaults() Reserve {
	d := DefaultReserve()
	if r.CPU.unset() {
		r.CPU = d.CPU
	}
	if r.Memory.unset() {
		r.Memory = d.Memory
	}
	return r
}

// CPUCap is the most cores a VM may be given on a host with this many.
//
// Never below one. A reserve large enough to leave nothing would otherwise make
// the cap zero, and a zero cap refuses every possible size — including one core
// on a machine that plainly has one to give. Refusing everything is the one
// answer that cannot be what the user meant.
func (r Reserve) CPUCap(hostCPUs int) int {
	if hostCPUs <= 0 {
		return 0
	}
	r = r.withDefaults()
	keep := r.CPU.Absolute
	if r.CPU.Percent > 0 {
		keep = hostCPUs * r.CPU.Percent / 100
	}
	return max(hostCPUs-keep, 1)
}

// MemoryCapGiB is the most memory a VM may be given, in the units colima's flag
// takes. The subtraction happens in bytes and the floor to GiB happens last:
// doing it the other way rounds the host's own size first, and the cap then
// differs from the machine by up to a gibibyte for no reason the user can see.
func (r Reserve) MemoryCapGiB(hostMemoryBytes int64) int {
	if hostMemoryBytes <= 0 {
		return 0
	}
	r = r.withDefaults()
	keep := int64(r.Memory.Absolute) * gibDivisor
	if r.Memory.Percent > 0 {
		keep = hostMemoryBytes * int64(r.Memory.Percent) / 100
	}
	return max(int((hostMemoryBytes-keep)/gibDivisor), 1)
}

// describe renders a reserve the way it was written, for the refusal message.
// A user who set "8 GiB" should not be told about a percentage devhub computed
// from it — they would go looking for a setting they never touched.
func (a Amount) describe(unit string) string {
	if a.Percent > 0 {
		return fmt.Sprintf("%d%%", a.Percent)
	}
	return fmt.Sprintf("%d%s", a.Absolute, unit)
}

// JSON renders a reserve for the settings document.
func (r Reserve) JSON() map[string]any {
	return map[string]any{
		"cpu":    r.CPU.json("cores"),
		"memory": r.Memory.json("gib"),
	}
}

// json renders one amount. A percentage stays a percentage: converting it to
// the cores it works out to on this machine would write the current host into
// the settings document, and the setting would then be wrong on the next one.
func (a Amount) json(absKey string) map[string]any {
	if a.Percent > 0 {
		return map[string]any{"percent": a.Percent}
	}
	return map[string]any{absKey: a.Absolute}
}

// NormalizeReserve validates a vm_reserve value from a request and returns what
// it means. It lives here rather than in the settings controller for the reason
// port normalization lives in portutil: the package that owns the concept owns
// the rule, so the value the settings endpoint accepts and the value the caps
// are computed from cannot come to disagree.
//
// Absent is the default, not an error — a settings document written before this
// existed is a valid one.
func NormalizeReserve(v any) (Reserve, error) {
	if v == nil {
		return DefaultReserve(), nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Reserve{}, fmt.Errorf("vm_reserve はオブジェクトで指定してください")
	}
	if err := rejectUnknownKeys(m, "vm_reserve", "cpu", "memory"); err != nil {
		return Reserve{}, err
	}
	var out Reserve
	var err error
	if out.CPU, err = normalizeAmount(m["cpu"], "cpu", "cores"); err != nil {
		return Reserve{}, err
	}
	if out.Memory, err = normalizeAmount(m["memory"], "memory", "gib"); err != nil {
		return Reserve{}, err
	}
	return out.withDefaults(), nil
}

// normalizeAmount reads one resource's reserve. absKey is the name the absolute
// form takes for this resource — cores for CPU, GiB for memory — because a
// reserve of "8" means nothing without its unit, and naming the unit in the key
// is what stops someone writing 8 meaning percent.
func normalizeAmount(v any, field, absKey string) (Amount, error) {
	if v == nil {
		return Amount{}, nil // unset; the default fills it in
	}
	m, ok := v.(map[string]any)
	if !ok {
		return Amount{}, fmt.Errorf("vm_reserve.%s はオブジェクトで指定してください（例: {\"percent\": 20} または {\"%s\": 2}）", field, absKey)
	}
	if err := rejectUnknownKeys(m, "vm_reserve."+field, "percent", absKey); err != nil {
		return Amount{}, err
	}

	pct, hasPct, err := intField(m, "percent", "vm_reserve."+field)
	if err != nil {
		return Amount{}, err
	}
	abs, hasAbs, err := intField(m, absKey, "vm_reserve."+field)
	if err != nil {
		return Amount{}, err
	}

	// Both is refused rather than resolved by precedence. Whichever one devhub
	// picked, the other would sit in the settings document looking like it was
	// in effect — and the user would be reading the wrong number when a refusal
	// surprised them.
	if hasPct && hasAbs {
		return Amount{}, fmt.Errorf("vm_reserve.%s は percent か %s のどちらか一方だけ指定してください", field, absKey)
	}
	if !hasPct && !hasAbs {
		return Amount{}, fmt.Errorf("vm_reserve.%s には percent か %s が要ります", field, absKey)
	}

	if hasPct {
		if pct < 0 || pct > maxReservePercent {
			return Amount{}, fmt.Errorf("vm_reserve.%s.percent は 0〜%d で指定してください（%d）", field, maxReservePercent, pct)
		}
		// Set even at zero: "reserve nothing" is a policy, not an absence, and
		// the default must not quietly replace it.
		return Amount{Percent: pct, Set: true}, nil
	}
	if abs < 0 {
		return Amount{}, fmt.Errorf("vm_reserve.%s.%s は 0 以上で指定してください（%d）", field, absKey, abs)
	}
	return Amount{Absolute: abs, Set: true}, nil
}

// intField reads a whole number. JSON numbers arrive as float64, and a
// fractional reserve is refused rather than truncated for the reason a
// fractional CPU count is: turning 2.5 into 2 answers a question nobody asked.
func intField(m map[string]any, key, where string) (int, bool, error) {
	raw, present := m[key]
	if !present || raw == nil {
		return 0, false, nil
	}
	var f float64
	switch n := raw.(type) {
	case float64:
		f = n
	case int:
		f = float64(n)
	default:
		return 0, false, fmt.Errorf("%s.%s は数値で指定してください", where, key)
	}
	if f != float64(int(f)) {
		return 0, false, fmt.Errorf("%s.%s は整数で指定してください（%v）", where, key, f)
	}
	return int(f), true, nil
}

// rejectUnknownKeys refuses a key devhub does not read. Ignoring one would mean
// a misspelled "percentage" leaves the default quietly in force while the
// settings screen shows the value the user thought they set.
func rejectUnknownKeys(m map[string]any, where string, allowed ...string) error {
	var bad []string
	for k := range m {
		if !contains(allowed, k) {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad) // map order is random; the message must not be
	return fmt.Errorf("%s に未知のキーがあります: %s（使えるのは %s）",
		where, strings.Join(bad, ", "), strings.Join(allowed, ", "))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
