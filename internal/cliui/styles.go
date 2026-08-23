// Package cliui holds the command-line presentation layer: semantic colors
// for run results, spikes, and compare verdicts. Lipgloss degrades to plain
// ASCII automatically on dumb terminals and non-TTY output, so piping and CI
// stay clean.
package cliui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	dimStyle    = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber
)

// Dim renders faint text (metadata lines).
func Dim(s string) string { return dimStyle.Render(s) }

// Err renders error text.
func Err(s string) string { return errStyle.Render(s) }

// Warn renders warning text.
func Warn(s string) string { return warnStyle.Render(s) }

// Ok renders success text.
func Ok(s string) string { return okStyle.Render(s) }

// Accent renders amber accent text (bottlenecks, correlated notes).
func Accent(s string) string { return accentStyle.Render(s) }

// VerdictColorize colors a compare verdict: REGRESSION red, ok dim.
func VerdictColorize(v string) string {
	switch v {
	case "REGRESSION":
		return errStyle.Render(v)
	case "ok":
		return dimStyle.Render(v)
	default:
		return v
	}
}

// SuccessColorize colors a success percentage by health: <90% red,
// <99% amber, otherwise plain.
func SuccessColorize(pct float64) string {
	s := strconv.FormatFloat(pct, 'f', 1, 64) + "%"
	switch {
	case pct < 90:
		return errStyle.Render(s)
	case pct < 99:
		return warnStyle.Render(s)
	default:
		return s
	}
}

// SpikeNoteColorize colors a correlated-spike note: "-only" notes are dim
// (masked), anything else is amber (correlated).
func SpikeNoteColorize(note string) string {
	switch {
	case strings.HasSuffix(note, "-only"):
		return dimStyle.Render(note)
	case note != "":
		return Accent(note)
	default:
		return note
	}
}
