package barrage

import (
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/codetesla51/barrage/internal/cliui"
)

// RunStats carries live per-runner counters so the CLI can log progress
// during a run instead of going silent until the final table.
type RunStats struct {
	HTTPFired  atomic.Uint64
	HTTPErr    atomic.Uint64
	DBFired    atomic.Uint64
	DBErr      atomic.Uint64
	RedisFired atomic.Uint64
	RedisErr   atomic.Uint64
	ScenLoops  atomic.Uint64
	ScenErr    atomic.Uint64
}

// StartLogger prints one structured status line every 5s until done closes.
// Output goes to stderr so report files and piped stdout stay clean.
func (s *RunStats) StartLogger(done <-chan struct{}, duration time.Duration) {
	if s == nil {
		return
	}
	start := time.Now()
	total := mmss(duration)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			elapsed := mmss(time.Since(start).Round(time.Second))
			fmt.Fprintf(os.Stderr, "  %s %s%s\n",
				cliui.Dim(elapsed+"/"+total),
				cliui.Dim("│"),
				s.segments(),
			)
		}
	}
}

// Summary returns the styled final totals line.
func (s *RunStats) Summary() string {
	if s == nil {
		return ""
	}
	return s.segments()
}

func (s *RunStats) segments() string {
	out := ""
	add := func(name string, fired, err uint64) {
		count := comma(int64(fired))
		errTxt := cliui.Dim("0 err")
		if err > 0 {
			errTxt = cliui.Err(comma(int64(err)) + " err")
		}
		out += fmt.Sprintf(" %s %s %s %s", cliui.Dim("│"), name,
			cliui.Accent(count), errTxt)
	}
	if v := s.HTTPFired.Load(); v > 0 {
		add("http", v, s.HTTPErr.Load())
	}
	if v := s.DBFired.Load(); v > 0 {
		add("db", v, s.DBErr.Load())
	}
	if v := s.RedisFired.Load(); v > 0 {
		add("redis", v, s.RedisErr.Load())
	}
	if v := s.ScenLoops.Load(); v > 0 {
		add("scen", v, s.ScenErr.Load())
	}
	return out
}

// mmss formats a duration as mm:ss (hh:mm:ss past an hour).
func mmss(d time.Duration) string {
	d = d.Round(time.Second)
	h := int64(d.Hours())
	m := int64(d.Minutes()) % 60
	sec := int64(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

// comma groups digits with commas: 1234567 → "1,234,567".
func comma(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
