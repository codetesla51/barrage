package cliui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbletea"
	"github.com/codetesla51/barrage"
)

// tickFreq balances "feels live" against CPU: 250ms is plenty for numbers
// that humans read.
const tickFreq = 250 * time.Millisecond

type tickMsg time.Time
type doneMsg struct{ err error }

// progressModel renders a compact inline status block under the run banner
// and replaces itself with the completion summary when the run ends. It owns
// no metrics — it samples barrage.RunProgress and renders.
type progressModel struct {
	prog     *barrage.RunProgress
	done     <-chan error
	err      error
	finished bool
	bar      progress.Model
}

func newProgressModel(prog *barrage.RunProgress, done <-chan error, width int) progressModel {
	p := progress.New(progress.WithSolidFill("#e0b45f"), progress.WithoutPercentage())
	p.Width = width
	return progressModel{prog: prog, done: done, bar: p}
}

func (m progressModel) Init() tea.Cmd {
	return tea.Batch(tick(), waitForDone(m.done))
}

func tick() tea.Cmd {
	return tea.Tick(tickFreq, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func waitForDone(done <-chan error) tea.Cmd {
	return func() tea.Msg { return doneMsg{err: <-done} }
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.bar.Width = max(10, msg.Width-4)
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case tickMsg:
		return m, tea.Batch(tick(), waitForDone(m.done))
	case doneMsg:
		m.finished = true
		m.err = msg.err
		return m, tea.Quit
	}
	bar, cmd := m.bar.Update(msg)
	m.bar = bar.(progress.Model)
	return m, cmd
}

func (m progressModel) View() string {
	snap := m.prog.Snapshot()
	var b strings.Builder

	stateLine := fmt.Sprintf("%s  %s / %s",
		stateStyle.Render(snap.State),
		dur(snap.Elapsed), dur(snap.Duration))
	if snap.Concurrency > 0 {
		stateLine += dimStyle.Render(fmt.Sprintf("  ·  concurrency %d", snap.Concurrency))
	}
	b.WriteString(stateLine + "\n")

	for _, r := range snap.Runners {
		b.WriteString(fmt.Sprintf("%-9s %8s reqs   p50 %6s   p99 %6s   ",
			r.Name, comma(int64(r.Requests)), dur(r.P50), dur(r.P99)))
		b.WriteString(SuccessColorize(r.SuccessPct) + "\n")
	}
	if len(snap.Runners) == 0 {
		b.WriteString(dimStyle.Render("warming up…") + "\n")
	}

	pct := 0.0
	if snap.Duration > 0 {
		pct = float64(snap.Elapsed) / float64(snap.Duration)
		if pct > 1 {
			pct = 1
		}
	}
	m.bar.SetPercent(pct)
	b.WriteString("\n" + m.bar.View() + "\n")
	b.WriteString(dimStyle.Render("q / ctrl+c abort") + "\n")
	return b.String()
}

// RunProgressUI drives the inline live view until the run finishes. It blocks
// until done receives; if the user quits early it exits the process with
// status 130, because runners cannot be cancelled mid-flight today — an
// orphaned run would keep firing load with no UI attached.
func RunProgressUI(prog *barrage.RunProgress, done <-chan error, width int) error {
	m, err := tea.NewProgram(
		newProgressModel(prog, done, width),
		tea.WithOutput(Stdout),
	).Run()
	if err != nil {
		return err // renderer failed; caller falls back to plain output
	}
	if pm, ok := m.(progressModel); ok && !pm.finished {
		osExit(130)
	}
	if pm, ok := m.(progressModel); ok {
		return pm.err
	}
	return nil
}
