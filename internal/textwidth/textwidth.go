// Package textwidth measures how many terminal columns a string occupies.
//
// text/tabwriter and fmt's %-Ns padding both size a cell by counting runes, so
// a cell holding CJK text is measured at half the width it is actually drawn
// at and every column after it drifts. The CLI's tables print user-supplied
// environment and component labels plus Japanese status reasons, so they need
// the rendered width, not the rune count.
//
// The East Asian Width table below is carried here on purpose rather than
// pulled in as a module: it is a few dozen lines, it never needs to be in
// lockstep with anything else, and a new third-party dependency would have to
// earn its place under the repo's supply-chain rules for the sake of one
// function.
package textwidth

import (
	"sort"
	"unicode"
)

// Width returns the number of terminal columns s occupies when printed.
//
// Covered:
//
//   - East Asian Wide (W) and Fullwidth (F) code points count as 2: CJK
//     ideographs, kana, hangul, the fullwidth ASCII forms, and the main emoji
//     blocks. Halfwidth katakana (U+FF61–U+FF9F) stays 1, as it is drawn.
//   - Combining marks, variation selectors and the other format code points
//     (ZWJ, ZWNJ, BOM) count as 0 — they advance no column — as do C0/C1
//     controls.
//   - Everything else counts as 1.
//
// Not covered, deliberately:
//
//   - East Asian Ambiguous (A) — ● ○ ★ ■ ① → — §, Greek, Cyrillic, box
//     drawing — counts as 1. Those are drawn 2 columns wide under a CJK
//     locale and 1 column wide elsewhere, and nothing in the process can tell
//     which the terminal picked. Deciding it from $LANG (what go-runewidth
//     does) would make the same table render differently for two people on
//     the same machine, so this stays deterministic and a table whose cells
//     hold ambiguous characters can still drift.
//   - Emoji presentation sequences (base + U+FE0F, e.g. ⚠️) count as their
//     base code point, so an ambiguous base still counts as 1.
//   - Width is summed per code point, not per grapheme cluster, so a ZWJ
//     sequence such as 👨‍👩‍👧 counts 6 where a terminal draws 2.
//   - The emoji ranges are block-granular, so the unassigned and narrow code
//     points scattered inside them count as 2.
func Width(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func runeWidth(r rune) int {
	switch {
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		// C0/C1 controls draw nothing. A cell holding \t or \n still breaks
		// the layout, exactly as it did under tabwriter — callers own that.
		return 0
	case r < 0x80:
		return 1
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		// Tested before the wide table on purpose: U+3099/U+309A sit inside
		// the kana range but are combining marks that advance no column.
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// interval is an inclusive code point range.
type interval struct{ lo, hi rune }

// wide holds the East Asian Wide/Fullwidth ranges, sorted and non-overlapping
// so isWide can binary-search them. Ranges are stated at block granularity
// where the block is uniformly wide; the few exceptions worth knowing are
// called out in the comments, because they are the ones that would silently
// double-count something narrow.
var wide = []interval{
	{0x1100, 0x115F},   // Hangul Jamo, initial consonants
	{0x2E80, 0x2EFF},   // CJK Radicals Supplement
	{0x2F00, 0x2FDF},   // Kangxi Radicals
	{0x2FF0, 0x2FFF},   // Ideographic Description Characters
	{0x3000, 0x303E},   // CJK Symbols and Punctuation; U+303F is narrow
	{0x3041, 0x33FF},   // Kana, Bopomofo, Hangul Compatibility Jamo, Kanbun, Enclosed CJK, CJK Compatibility
	{0x3400, 0x4DBF},   // CJK Unified Ideographs Extension A; the Yijing block above it is narrow
	{0x4E00, 0x9FFF},   // CJK Unified Ideographs
	{0xA000, 0xA4CF},   // Yi Syllables and Yi Radicals
	{0xA960, 0xA97F},   // Hangul Jamo Extended-A
	{0xAC00, 0xD7A3},   // Hangul Syllables
	{0xF900, 0xFAFF},   // CJK Compatibility Ideographs
	{0xFE10, 0xFE19},   // Vertical Forms
	{0xFE30, 0xFE6F},   // CJK Compatibility Forms, Small Form Variants
	{0xFF01, 0xFF60},   // Fullwidth ASCII forms; U+FF61–U+FF9F halfwidth katakana stays 1
	{0xFFE0, 0xFFE6},   // Fullwidth signs
	{0x16FE0, 0x16FFF}, // Ideographic symbols and punctuation
	{0x17000, 0x18AFF}, // Tangut, Tangut Components
	{0x18B00, 0x18CFF}, // Khitan Small Script
	{0x18D00, 0x18D8F}, // Tangut Supplement
	{0x1AFF0, 0x1AFFF}, // Kana Extended-B
	{0x1B000, 0x1B16F}, // Kana Supplement, Kana Extended-A, Small Kana Extension
	{0x1B170, 0x1B2FF}, // Nushu
	{0x1F004, 0x1F004}, // Mahjong tile red dragon
	{0x1F0CF, 0x1F0CF}, // Playing card black joker
	{0x1F18E, 0x1F18E}, // Negative squared AB
	{0x1F191, 0x1F19A}, // Squared CL through squared VS
	{0x1F200, 0x1F2FF}, // Enclosed Ideographic Supplement
	{0x1F300, 0x1F64F}, // Miscellaneous Symbols and Pictographs, Emoticons
	{0x1F680, 0x1F6FF}, // Transport and Map Symbols
	{0x1F7E0, 0x1F7EB}, // Large coloured circles and squares
	{0x1F7F0, 0x1F7F0}, // Heavy equals sign
	{0x1F900, 0x1F9FF}, // Supplemental Symbols and Pictographs
	{0x1FA70, 0x1FAFF}, // Symbols and Pictographs Extended-A
	{0x20000, 0x2FFFD}, // CJK Unified Ideographs Extension B and later
	{0x30000, 0x3FFFD}, // CJK Unified Ideographs Extension G and later
}

func isWide(r rune) bool {
	i := sort.Search(len(wide), func(i int) bool { return r <= wide[i].hi })
	return i < len(wide) && r >= wide[i].lo
}
