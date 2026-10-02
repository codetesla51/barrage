package barrage

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/codetesla51/barrage/internal/cliui"
)

// ProgressModel is a Bubble Tea live-progress view for a run: one runway lane
// per runner, each showing a head that travels while requests flow, plus the
// same per-runner counters as the plain logger.
// It reads shared RunStats, so the zero value is useless — build it with
// NewProgressModel. The caller quits it (Quit) once the run ends.
type ProgressModel struct {
	stats *RunStats
	total time.Duration
	start time.Time
	spin  spinner.Model
	lanes *laneTracker
}

type progressTickMsg time.Time

// laneState is one runner's counters plus the previous sample, so the rate
// can be derived as a delta between ticks.
type laneState struct {
	name      string
	fired     *atomic.Uint64
	errs      *atomic.Uint64
	prevFired uint64
	prevErr   uint64
	hasPrev   bool
	rate      float64
	errDelta  uint64
}

// laneTracker owns the mutable per-tick state. ProgressModel is copied by
// value through tea.Model, so anything that changes between ticks lives here.
type laneTracker struct {
	lanes  []*laneState
	lastAt time.Time
	frame  int
}

const (
	// laneWidth is the track length in cells.
	laneWidth = 30
	// trailMax is the longest dot trail a lane can draw.
	trailMax = 7
	// rateCeiling is the rate that maps to a full trail, so a fast cache does
	// not need a longer track than a slow one.
	rateCeiling = 300.0
	// fastRate is the rate above which a lane steps two cells per tick.
	fastRate = 150.0
)

// lanePhases offsets each lane's head so they do not march in lockstep.
// A var, not a const: a slice literal cannot be a constant.
var lanePhases = []int{0, 10, 20, 4}

// NewProgressModel binds a live view to shared counters and a run duration.
func NewProgressModel(stats *RunStats, total time.Duration) ProgressModel {
	return ProgressModel{
		stats: stats,
		total: total,
		start: time.Now(),
		spin:  spinner.New(spinner.WithSpinner(spinner.Dot)),
		lanes: newLaneTracker(stats),
	}
}

func newLaneTracker(stats *RunStats) *laneTracker {
	t := &laneTracker{lastAt: time.Now()}
	if stats == nil {
		return t
	}
	t.lanes = []*laneState{
		{name: "http", fired: &stats.HTTPFired, errs: &stats.HTTPErr},
		{name: "db", fired: &stats.DBFired, errs: &stats.DBErr},
		{name: "redis", fired: &stats.RedisFired, errs: &stats.RedisErr},
		{name: "scen", fired: &stats.ScenLoops, errs: &stats.ScenErr},
	}
	return t
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
	switch msg := msg.(type) {
	case progressTickMsg:
		if m.lanes != nil {
			m.lanes.sample(time.Time(msg))
		}
		return m, progressTick()
	default:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
}

// View renders the self-updating progress block: a clock line, then one lane
// per runner that has fired. Nil stats render empty.
func (m ProgressModel) View() string {
	if m.stats == nil {
		return ""
	}
	elapsed := mmss(time.Since(m.start).Round(time.Second))
	lanes := m.lanes.lines()
	var b strings.Builder
	b.WriteString("  " + m.spin.View() + " ")
	b.WriteString(cliui.Dim(elapsed + "/" + mmss(m.total)))
	for _, line := range lanes {
		b.WriteString("\n")
		b.WriteString(line)
	}
	// Tea's standard renderer moves up (lines-1) before repainting, so the
	// view must always end with exactly one trailing newline or the block
	// creeps down the screen a line every tick.
	b.WriteString("\n")
	return b.String()
}

// sample records a fresh read of every runner's counters and derives each
// rate from the delta since the previous sample.
func (t *laneTracker) sample(at time.Time) {
	dt := at.Sub(t.lastAt).Seconds()
	if dt <= 0 {
		return
	}
	t.lastAt = at
	t.frame++
	for _, l := range t.lanes {
		f, e := l.fired.Load(), l.errs.Load()
		if l.hasPrev {
			// counters only ever grow, but guard anyway: a negative rate would
			// feed a NaN into the log scale and draw a garbage trail
			l.rate, l.errDelta = 0, 0
			if f >= l.prevFired {
				l.rate = float64(f-l.prevFired) / dt
			}
			if e >= l.prevErr {
				l.errDelta = e - l.prevErr
			}
		}
		l.prevFired, l.prevErr, l.hasPrev = f, e, true
	}
}

// lines draws one row per runner that has fired. Idle runners are dropped:
// tea only overwrites the lines it is given, so a short block leaves the tail
// of the previous frame behind (that is what produced a stray half-drawn
// "db" row mid-run). Nothing on screen depends on the count being constant.
func (t *laneTracker) lines() []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.lanes))
	for i, l := range t.lanes {
		if l.fired.Load() == 0 {
			continue
		}
		phase := lanePhases[i%len(lanePhases)]
		step := 1
		if l.rate > fastRate {
			step = 2
		}
		pos := (t.frame*step + phase) % laneWidth
		out = append(out, l.line(pos, trailLen(l.rate)))
	}
	return out
}

// line builds one "name track count rate err" row. Every field is fixed width
// so the block does not jitter as the numbers change.
func (l *laneState) line(pos, trail int) string {
	cells := make([]rune, laneWidth)
	for i := range cells {
		cells[i] = '·'
	}
	for i := 0; i < trail; i++ {
		if idx := pos - i; idx >= 0 {
			cells[idx] = '•'
		}
	}
	cells[pos] = '>'

	markStyle := cliui.Accent
	if l.errDelta > 0 {
		markStyle = cliui.Err
		cells[pos] = 'X'
	}
	// style each run separately: a lipgloss reset mid-string would end the dim
	// run, so the head has to carry its own escape pair
	track := cliui.Dim(string(cells[:pos])) + markStyle(string(cells[pos:pos+1])) + cliui.Dim(string(cells[pos+1:]))

	errTxt := cliui.Dim("      ")
	if l.errDelta > 0 {
		errTxt = cliui.Err(fmt.Sprintf("%d err", l.errDelta))
	}
	return fmt.Sprintf("  %-5s %s %s %4.0f/s %s",
		l.name, track, cliui.Accent(comma(int64(l.fired.Load()))), l.rate, errTxt)
}

// trailLen maps a request rate to a 1..trailMax dot trail on a log scale,
// so a 300/s cache and a 20/s database are both readable on one track.
func trailLen(rate float64) int {
	if rate <= 0 {
		return 1
	}
	n := 1 + int(math.Log(1+rate)/math.Log(1+rateCeiling)*trailMax)
	if n < 1 {
		return 1
	}
	if n > trailMax {
		return trailMax
	}
	return n
}
