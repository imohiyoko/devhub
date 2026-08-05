package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	envsctl "github.com/imohiyoko/devhub/internal/controllers/envs"
	"github.com/imohiyoko/devhub/internal/textwidth"
)

// fixtureWidths states, by hand, how many terminal columns each non-ASCII
// fixture string occupies. The alignment checks below measure with these
// rather than with textwidth.Width on purpose: a test that measures the
// output with the same function that produced it agrees with a broken width
// function and passes on exactly the bug it exists to catch.
var fixtureWidths = map[string]int{
	"フルスタック検証環境":         20,
	"devhub 検証環境":        15,
	"CLI ポート指定アプリの検証例":   28,
	"検証環境コンポーネント":        22,
	"監視できるポートが宣言されていません": 36,
	"ポートを CLI 引数で受けるアプリ": 31,
	"共有キャッシュ":            14,
	"かな":                 4,
	"カタカナ":               8,
	"한글":                 4,
	"ｾﾞﾝﾊﾝ混在":            9,
	"ＦＵＬＬ":               8,
	"🚀 emoji":            8,
	"混在 mixed":           10,
}

// declaredWidth is the fixtures' own notion of display width: one column per
// byte for ASCII (independently true), and the hand-written value otherwise.
func declaredWidth(t *testing.T, s string) int {
	t.Helper()
	if w, ok := fixtureWidths[s]; ok {
		return w
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			t.Fatalf("fixture %q is not ASCII and has no declared width", s)
		}
	}
	return len(s)
}

// TestFixtureWidthsMatchTextwidth ties the hand-written widths to the package
// under test. It is the one place the two are compared, so a typo in the
// table surfaces here instead of quietly weakening every alignment check.
func TestFixtureWidthsMatchTextwidth(t *testing.T) {
	for s, want := range fixtureWidths {
		if got := textwidth.Width(s); got != want {
			t.Errorf("textwidth.Width(%q) = %d, fixture declares %d", s, got, want)
		}
	}
}

// render runs printTable and splits the result into lines.
func render(header []string, rows [][]string) []string {
	var buf bytes.Buffer
	printTable(&buf, header, rows)
	return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
}

// unobservable marks a column whose start cannot be read off the rendered
// line, which is only ever an empty cell: it prints nothing, so it has no
// visible left edge to compare.
const unobservable = -1

// columnStarts walks a rendered line, consuming each cell and the run of
// spaces after it, and reports the display column every cell begins at.
// Equal starts across every line is the definition of "the columns line up",
// which is what silently broke when a cell held CJK text.
func columnStarts(t *testing.T, line string, cells []string) []int {
	t.Helper()
	starts := make([]int, 0, len(cells))
	col, rest := 0, line
	for i, cell := range cells {
		if cell == "" {
			starts = append(starts, unobservable)
			continue
		}
		starts = append(starts, col)
		if !strings.HasPrefix(rest, cell) {
			t.Fatalf("line %q: cell %d (%q) is not where the layout says it is (found %q)", line, i, cell, rest)
		}
		rest = rest[len(cell):]
		col += declaredWidth(t, cell)
		pad := len(rest) - len(strings.TrimLeft(rest, " "))
		rest, col = rest[pad:], col+pad
	}
	if rest != "" {
		t.Fatalf("line %q: unconsumed trailing content %q", line, rest)
	}
	return starts
}

// assertAligned checks that every line puts column k at the same display
// column, header included.
func assertAligned(t *testing.T, name string, header []string, rows [][]string) []string {
	t.Helper()
	lines := render(header, rows)
	if len(lines) != len(rows)+1 {
		t.Fatalf("%s: got %d lines for %d rows", name, len(lines), len(rows))
	}
	want := columnStarts(t, lines[0], header)
	compared := 0
	for i, row := range rows {
		got := columnStarts(t, lines[i+1], row)
		for k := range want {
			if k >= len(got) || got[k] == unobservable || want[k] == unobservable {
				continue
			}
			compared++
			if got[k] != want[k] {
				t.Errorf("%s: row %d column %d starts at display column %d, header has it at %d\n%s\n%s",
					name, i, k, got[k], want[k], lines[0], lines[i+1])
			}
		}
	}
	// Guard the guard: a fixture of nothing but empty cells would skip every
	// comparison and pass without proving anything.
	if compared == 0 {
		t.Fatalf("%s: no column starts were comparable", name)
	}
	return lines
}

// TestPrintTableAlignsFullWidthCells is the regression: text/tabwriter sized
// "フルスタック検証環境" as 10 (its rune count) instead of the 20 columns it is
// drawn at, so PROCESSES and LIVE landed 10 columns early on that row alone.
func TestPrintTableAlignsFullWidthCells(t *testing.T) {
	assertAligned(t, "env list", []string{"ID", "NAME", "PROCESSES", "LIVE"}, [][]string{
		{"local-full-stack", "フルスタック検証環境", "2", "-"},
		{"devhub-verify", "devhub 検証環境", "1", ":8765"},
		{"cli-port-verify", "CLI ポート指定アプリの検証例", "1", "-"},
		{"ascii-only", "plain name", "3", "-"},
	})
}

// TestPrintTableAlignsMixedWidthScripts keeps the alignment honest for the
// other scripts a label can legitimately hold, so a fix aimed at kanji is not
// quietly kana- or hangul-shaped. Halfwidth katakana is in there because it
// is the case that must *not* be widened, and the empty cell because a middle
// column can be blank.
func TestPrintTableAlignsMixedWidthScripts(t *testing.T) {
	assertAligned(t, "mixed scripts", []string{"A", "B", "C"}, [][]string{
		{"ascii", "plain", "x"},
		{"かな", "カタカナ", "x"},
		{"한글", "ｾﾞﾝﾊﾝ混在", "x"},
		{"ＦＵＬＬ", "🚀 emoji", "x"},
		{"混在 mixed", "", "x"},
	})
}

// TestPrintTableExactLayout pins the concrete spacing, with a full-width cell
// wide enough to set its column's width — otherwise the header would decide
// it and the layout would look identical whether widths were measured in
// columns or in runes. Each column is as wide as its widest cell plus a
// two-space gutter, which is what the tabwriter this replaced was configured
// to do, and the 22-column label is what makes the gaps below what they are.
func TestPrintTableExactLayout(t *testing.T) {
	sp := func(n int) string { return strings.Repeat(" ", n) }
	got := strings.Join(render(
		[]string{"STATE", "COMPONENT", "KIND"},
		[][]string{
			{"running", "検証環境コンポーネント", "compose_service"},
			{"stopped", "api", "host_process"},
		},
	), "\n")
	want := strings.Join([]string{
		"STATE" + sp(4) + "COMPONENT" + sp(15) + "KIND",
		"running" + sp(2) + "検証環境コンポーネント" + sp(2) + "compose_service",
		"stopped" + sp(2) + "api" + sp(21) + "host_process",
	}, "\n")
	if got != want {
		t.Errorf("printTable output:\n%s\nwant:\n%s", got, want)
	}
}

// TestPrintTableDropsTrailingSpaces guards a row whose last cell is empty —
// `env status` prints one whenever a component's state needs no explanation.
func TestPrintTableDropsTrailingSpaces(t *testing.T) {
	for _, line := range render([]string{"A", "B"}, [][]string{{"filled", ""}, {"x", "y"}}) {
		if strings.TrimRight(line, " ") != line {
			t.Errorf("line %q ends in whitespace", line)
		}
	}
}

// TestEnvListRowsAlign runs the real `devhub env list` layout, whose NAME
// column is a user-supplied environment name and is Japanese in the example
// definitions devhub ships with.
func TestEnvListRowsAlign(t *testing.T) {
	rows := envListRows([]envsctl.EnvStatus{
		{ID: "local-full-stack", Name: "フルスタック検証環境", Processes: 2},
		{ID: "devhub-verify", Name: "devhub 検証環境", Processes: 1, LivePorts: []int{8765}},
		{ID: "cli-port-verify", Name: "CLI ポート指定アプリの検証例", Processes: 1},
	})
	lines := assertAligned(t, "envListRows", []string{"ID", "NAME", "PROCESSES", "LIVE"}, rows)
	if !strings.Contains(lines[2], ":8765") {
		t.Errorf("live ports missing from %q", lines[2])
	}
}

// TestEnvComponentRowsAlign runs the real `devhub env status` layout. DETAIL
// holds the Japanese reason a state could not be observed and SCOPE sits
// right before it, so a mismeasured COMPONENT label pushed both off.
func TestEnvComponentRowsAlign(t *testing.T) {
	rows := envComponentRows([]envsctl.ComponentReport{
		{Label: "PostgreSQL", Kind: "host_process", State: "unknown", Reason: "監視できるポートが宣言されていません"},
		{Label: "ポートを CLI 引数で受けるアプリ", Kind: "host_process", State: "stopped"},
		{Label: "共有キャッシュ", Kind: "compose_service", Shared: true, State: "running"},
	})
	lines := assertAligned(t, "envComponentRows", []string{"STATE", "COMPONENT", "KIND", "SCOPE", "DETAIL"}, rows)
	if !strings.Contains(lines[3], "shared") {
		t.Errorf("shared scope missing from %q", lines[3])
	}
}
