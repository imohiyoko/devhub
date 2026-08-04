package textwidth

import (
	"testing"
	"unicode/utf8"
)

// TestWidth pins the cases the CLI tables actually depend on. The interesting
// ones are those where the width differs from the rune count, since that
// difference is the whole reason this package exists. Invisible code points
// are spelled as escapes so the fixtures survive an editor normalising them.
func TestWidth(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "host_process", 12},
		{"hiragana", "かな", 4},
		{"katakana", "カタカナ", 8},
		{"kanji", "検証環境", 8},
		{"mixed", "devhub 検証環境", 15},
		{"the reason string env status prints", "監視できるポートが宣言されていません", 36},
		{"fullwidth parens", "（command）", 11},
		{"fullwidth latin and digits", "ＡＢ１２", 8},
		{"halfwidth katakana stays narrow", "ｶﾀｶﾅ", 4},
		{"hangul syllables", "한글", 4},
		{"hangul jamo initial consonant", "ᄀ", 2},
		{"ideographic space", "　", 2},
		{"ideographic half fill space is narrow", "〿", 1},
		{"cjk extension B", "\U00020000", 2},
		{"emoji", "🚀", 2},
		{"emoji in the supplemental block", "🧪", 2},
		{"decomposed kana: the combining mark adds nothing", "が", 2},
		{"decomposed latin: the combining mark adds nothing", "é", 1},
		{"variation selector adds nothing", "⚠️", 1},
		{"zero width joiner adds nothing", "‍", 0},
		{"c0 controls draw nothing", "\x00\x01", 0},
	}
	for _, c := range cases {
		if got := Width(c.s); got != c.want {
			t.Errorf("%s: Width(%q) = %d, want %d", c.name, c.s, got, c.want)
		}
	}
}

// TestWidthDiffersFromRuneCount is the regression this package exists for: a
// rune count (what text/tabwriter and fmt's %-Ns padding both use) reports
// half the truth for CJK, which is exactly how far the table drifted.
func TestWidthDiffersFromRuneCount(t *testing.T) {
	const s = "フルスタック検証環境"
	if runes := utf8.RuneCountInString(s); runes != 10 {
		t.Fatalf("fixture changed: rune count = %d", runes)
	}
	if got := Width(s); got != 20 {
		t.Errorf("Width(%q) = %d, want 20 (twice the rune count)", s, got)
	}
}

// TestAmbiguousCountsAsNarrow documents the deliberate limit instead of
// leaving it to be discovered: these are drawn 2 columns wide under a CJK
// locale and this package still calls them 1. If that trade is ever
// revisited, this test is what has to change with it.
func TestAmbiguousCountsAsNarrow(t *testing.T) {
	for _, s := range []string{"●", "○", "★", "■", "①", "→", "—", "±"} {
		if got := Width(s); got != 1 {
			t.Errorf("Width(%q) = %d, want 1 (East Asian Ambiguous is not covered)", s, got)
		}
	}
}

// TestWideTableIsSortedAndDisjoint guards the invariant isWide's binary search
// relies on: a range added out of order would make lookups silently miss.
func TestWideTableIsSortedAndDisjoint(t *testing.T) {
	for i, iv := range wide {
		if iv.lo > iv.hi {
			t.Errorf("wide[%d] = %#v: lo > hi", i, iv)
		}
		if i > 0 && iv.lo <= wide[i-1].hi {
			t.Errorf("wide[%d] (%#v) overlaps or precedes wide[%d] (%#v)", i, iv, i-1, wide[i-1])
		}
	}
}

// TestIsWideFindsEveryBoundary walks each range's edges and the code points
// just outside them, so an off-by-one in the table is caught at the boundary
// rather than by whichever label happens to land on it.
func TestIsWideFindsEveryBoundary(t *testing.T) {
	inside := func(r rune) bool {
		for _, iv := range wide {
			if r >= iv.lo && r <= iv.hi {
				return true
			}
		}
		return false
	}
	for _, iv := range wide {
		for _, r := range []rune{iv.lo - 1, iv.lo, iv.hi, iv.hi + 1} {
			if got, want := isWide(r), inside(r); got != want {
				t.Errorf("isWide(%U) = %v, want %v", r, got, want)
			}
		}
	}
}
