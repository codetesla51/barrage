// Package cliui holds the command-line presentation layer: semantic colors
// for run results, spikes, and compare verdicts, plus the table renderer.
// Lipgloss degrades to plain ASCII automatically on dumb terminals and
// non-TTY output, so piping and CI stay clean.
package cliui

import (
	"strings"
	"unicode/utf8"
)

// Align controls a column's text alignment.
type Align int

const (
	// Left is the default: names, statuses, verdicts.
	Left Align = iota
	// Right for numbers, so magnitudes line up on the decimal.
	Right
)

// Column describes one table column. Name doubles as the header, so units go
// in the name ("P99", "SUCCESS") and a wider one ("HTTP_P99 (ms)") is fine.
type Column struct {
	Name  string
	Align Align
	// Gap is the minimum spaces between this column and the next. Zero
	// defaults to 2.
	Gap int
}

// Table renders aligned columns with light rules. Build rows with NewRow so
// cell widths are measured on visible text, never on escape bytes.
type Table struct {
	cols []Column
	rows [][]string
}

// NewTable starts a table. Column order matches every row.
func NewTable(cols ...Column) *Table { return &Table{cols: cols} }

// Row appends one record. Short rows are padded with empty cells; extra
// cells are ignored, so a caller cannot break the layout by over-adding.
func (t *Table) Row(cells ...string) *Table {
	t.rows = append(t.rows, cells)
	return t
}

// Len reports how many rows were added.
func (t *Table) Len() int { return len(t.rows) }

// Render draws the table: a top rule, the header, a rule, the rows, and a
// bottom rule. Empty tables render nothing, so callers can build one
// unconditionally and print it conditionally.
func (t *Table) Render() string {
	if len(t.rows) == 0 {
		return ""
	}
	n := len(t.cols)
	widths := make([]int, n)
	head := make([]string, n)
	for i, c := range t.cols {
		head[i] = c.Name
		widths[i] = VisibleLen(c.Name)
	}
	for _, r := range t.rows {
		for i := 0; i < n && i < len(r); i++ {
			if w := VisibleLen(r[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}

	// totalWidth already includes every inter-column gap.
	rule := Dim(strings.Repeat("─", t.totalWidth(widths)))

	var b strings.Builder
	b.WriteString(rule)
	b.WriteByte('\n')
	b.WriteString(Dim(t.line(head, widths)))
	b.WriteByte('\n')
	b.WriteString(rule)
	for _, r := range t.rows {
		b.WriteByte('\n')
		b.WriteString(t.line(r, widths))
	}
	b.WriteByte('\n')
	b.WriteString(rule)
	return b.String()
}

func (t *Table) line(cells []string, widths []int) string {
	var b strings.Builder
	for i, col := range t.cols {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		pad := widths[i] - VisibleLen(cell)
		if col.Align == Right {
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString(cell)
		} else {
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", pad))
		}
		if i < len(t.cols)-1 {
			b.WriteString(strings.Repeat(" ", t.gapAfter(i)))
		}
	}
	return b.String()
}

// totalWidth is the summed column width plus every inter-column gap, i.e. the
// rule's length without the trailing gap.
func (t *Table) totalWidth(widths []int) int {
	w := 0
	for i := range t.cols {
		w += widths[i]
		if i < len(t.cols)-1 {
			w += t.gapAfter(i)
		}
	}
	return w
}

func (t *Table) gapAfter(i int) int {
	if g := t.cols[i].Gap; g > 0 {
		return g
	}
	return 2
}

// VisibleLen counts runes, not bytes: the header and cells contain box-drawing
// and µs arrows, and UTF-8 would otherwise make every column measure wide.
// ANSI escapes are counted as zero so styled cells still align.
func VisibleLen(s string) int {
	n := 0
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			// CSI sequences end on a letter; anything else is still payload
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			n++
		}
	}
	if n == 0 && utf8.RuneCountInString(s) > 0 {
		return utf8.RuneCountInString(s)
	}
	return n
}
