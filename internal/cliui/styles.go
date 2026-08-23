// Package cliui holds the command-line presentation layer: lipgloss styles
// and the optional inline live-progress view rendered by Bubble Tea during
// `barrage run`. It is strictly presentation — all load generation and
// metrics live in the core package.
package cliui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Semantic styles. Lipgloss degrades colors automatically on dumb/non-TTY
// output, so piping to a file stays clean ASCII.
var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	stateStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // amber, matches barrage's accent
)

// Title renders bold text (used for section headers).
func Title(s string) string { return titleStyle.Render(s) }

// Dim renders faint text (metadata lines).
func Dim(s string) string { return dimStyle.Render(s) }

// Err renders error text.
func Err(s string) string { return errStyle.Render(s) }

// Warn renders warning text.
func Warn(s string) string { return warnStyle.Render(s) }

// Ok renders success text.
func Ok(s string) string { return okStyle.Render(s) }

// VerdictColorize colors a compare verdict: REGRESSION in red, ok dim.
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
// (masked), correlated ones are amber.
func SpikeNoteColorize(note string) string {
	if strings.HasSuffix(note, "-only") {
		return dimStyle.Render(note)
	}
	if note != "" {
		return stateStyle.Render(note)
	}
	return note
}

// Interactive decides whether the run command should render live progress.
// It is only ever enabled when explicitly requested AND the environment can
// handle it — CI, pipes, JSON output, and --no-interactive always get plain
// output.
func Interactive(requested, jsonOut bool, stdoutTTY bool, env map[string]string) bool {
	if !requested || jsonOut || !stdoutTTY {
		return false
	}
	if env["CI"] != "" || env["TF_BUILD"] != "" || env["GITHUB_ACTIONS"] == "true" {
		return false
	}
	if strings.EqualFold(env["TERM"], "dumb") {
		return false
	}
	return true
}

// IsTTY reports whether the file descriptor is a terminal.
func IsTTY(fd uintptr) bool {
	return term.IsTerminal(int(fd))
}
