package barrage

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/codetesla51/barrage/internal/cliui"
)

// ProgressModel is a Bubble Tea live-progress view for a run: spinner,
// elapsed/total clock, and the same per-runner counters as the plain logger.
// It reads shared RunStats, so the zero value is useless — build it with
// NewProgressModel. The caller quits it (Quit) once the run ends.
type ProgressModel struct {
	stats *RunStats
	total time.Duration
	start time.Time
	spin  spinner.Model
}

type progressTickMsg time.Time

// NewProgressModel binds a live view to shared counters and a run duration.
func NewProgressModel(stats *RunStats, total time.Duration) ProgressModel {
	return ProgressModel{
		stats: stats,
		total: total,
		start: time.Now(),
		spin:  spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
}

func progressTick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg {
		return progressTickMsg(t)
	})
}

// Init starts the spinner and the refresh ticker.
func (m ProgressModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, progressTick())
}

// Update refreshes on ticks and forwards spinner messages.
func (m ProgressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case progressTickMsg:
		return m, progressTick()
	default:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
}

// View renders one self-updating status line. Nil stats render empty.
func (m ProgressModel) View() string {
	if m.stats == nil {
		return ""
	}
	elapsed := mmss(time.Since(m.start).Round(time.Second))
	return fmt.Sprintf("  %s %s%s", m.spin.View(),
		cliui.Dim(elapsed+"/"+mmss(m.total)), m.stats.segments())
}
