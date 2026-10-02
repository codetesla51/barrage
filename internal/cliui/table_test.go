package cliui

import (
	"strings"
	"testing"
)

// A styled cell must measure on visible text, or every column to its right
// drifts by the length of the escape codes.
func TestVisibleLenIgnoresANSI(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"http", 4},
		{"100.0%", 6},
		{"\x1b[31mBROKEN\x1b[0m", 6},
		{"\x1b[2m·\x1b[0m", 1},
	}
	for _, c := range cases {
		if got := VisibleLen(c.in); got != c.want {
			t.Errorf("VisibleLen(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// Multi-byte cells (µs durations, box rules) must count as one column each.
func TestVisibleLenCountsRunes(t *testing.T) {
	if got := VisibleLen("753.053µs"); got != 9 {
		t.Errorf("VisibleLen(µs duration) = %d, want 9 runes", got)
	}
}

func TestTableAlignsColumns(t *testing.T) {
	tb := NewTable(
		Column{Name: "RUNNER"},
		Column{Name: "REQUESTS", Align: Right},
		Column{Name: "SUCCESS", Align: Right},
	)
	tb.Row("http", "45", "100.0%")
	tb.Row("redis", "12345", "96.2%")

	lines := strings.Split(strings.TrimRight(tb.Render(), "\n"), "\n")
	// rule / header / rule / 2 rows / rule
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines, got %d:\n%s", len(lines), tb.Render())
	}
	// Right-aligned: both request counts end at the same column.
	iHTTP := strings.Index(lines[3], "45")
	iRedis := strings.Index(lines[4], "12345")
	if iHTTP+len("45") != iRedis+len("12345") {
		t.Errorf("numbers not right-aligned: %q vs %q", lines[3], lines[4])
	}
	// Every row must be the same visible width, or the table looks ragged.
	w := VisibleLen(lines[2])
	for i, l := range lines[1:] {
		if got := VisibleLen(l); got != w {
			t.Errorf("line %d width %d, want %d: %q", i+1, got, w, l)
		}
	}
}

func TestTableEmptyRendersNothing(t *testing.T) {
	tb := NewTable(Column{Name: "RUNNER"}, Column{Name: "P99", Align: Right})
	if got := tb.Render(); got != "" {
		t.Errorf("empty table should render nothing, got %q", got)
	}
	if tb.Len() != 0 {
		t.Errorf("expected 0 rows, got %d", tb.Len())
	}
}

// A short row must pad, not shift, and an over-long row must be truncated to
// the declared column count — otherwise the layout breaks.
func TestTableHandlesRaggedRows(t *testing.T) {
	tb := NewTable(
		Column{Name: "RUNNER"},
		Column{Name: "VERDICT"},
		Column{Name: "NOTE"},
	)
	tb.Row("http", "ok")
	tb.Row("db", "BROKEN", "5xx", "extra")

	lines := strings.Split(strings.TrimRight(tb.Render(), "\n"), "\n")
	w := VisibleLen(lines[2])
	for i, l := range lines[1:] {
		if got := VisibleLen(l); got != w {
			t.Errorf("line %d width %d, want %d: %q", i+1, got, w, l)
		}
	}
}