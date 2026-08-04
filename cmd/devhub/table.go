package main

import (
	"io"
	"strings"

	"github.com/imohiyoko/devhub/internal/textwidth"
)

// tableGutter is the number of spaces between two columns. It is the padding
// the text/tabwriter this replaced was configured with, so an ASCII-only table
// comes out byte-for-byte as before.
const tableGutter = 2

// printTable writes header and rows as a left-aligned table.
//
// It exists because text/tabwriter sizes a cell by counting runes: a label
// like "フルスタック検証環境" is 10 runes but 20 columns, so tabwriter padded it
// 10 columns short and every column to its right drifted by that much. The
// tables here print user-supplied labels and Japanese status reasons, so that
// was the normal case rather than an edge one. Sizing by rendered width is the
// only fix — tabwriter cannot be taught a width function.
//
// The last column is written unpadded and trailing spaces are dropped, so a
// row whose final cell is empty does not end in whitespace. Cells containing
// a tab or newline break the layout, exactly as they did before.
func printTable(out io.Writer, header []string, rows [][]string) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = textwidth.Width(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], textwidth.Width(cell))
			}
		}
	}
	var b strings.Builder
	writeTableRow(&b, header, widths)
	for _, row := range rows {
		writeTableRow(&b, row, widths)
	}
	_, _ = io.WriteString(out, b.String())
}

func writeTableRow(b *strings.Builder, cells []string, widths []int) {
	var line strings.Builder
	for i, cell := range cells {
		line.WriteString(cell)
		if i == len(cells)-1 {
			break
		}
		pad := tableGutter
		if i < len(widths) {
			pad += widths[i] - textwidth.Width(cell)
		}
		line.WriteString(strings.Repeat(" ", pad))
	}
	b.WriteString(strings.TrimRight(line.String(), " "))
	b.WriteByte('\n')
}
