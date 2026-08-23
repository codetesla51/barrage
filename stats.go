package barrage

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"
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

// StartLogger prints one status line every interval until done is closed.
// It writes to stderr so report files and piped stdout stay clean.
func (s *RunStats) StartLogger(done <-chan struct{}, duration time.Duration) {
	if s == nil {
		return
	}
	start := time.Now()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			fmt.Fprintf(os.Stderr, "[barrage] %s / %s%s\n",
				time.Since(start).Round(time.Second), duration, s.line())
		}
	}
}

// Summary returns the final totals line.
func (s *RunStats) Summary() string {
	if s == nil {
		return ""
	}
	return "totals " + s.line()[2:] // strip leading " ·" from line()
}

func (s *RunStats) line() string {
	out := ""
	if s.HTTPFired.Load() > 0 {
		out += fmt.Sprintf(" · http %d (%d err)", s.HTTPFired.Load(), s.HTTPErr.Load())
	}
	if s.DBFired.Load() > 0 {
		out += fmt.Sprintf(" · db %d (%d err)", s.DBFired.Load(), s.DBErr.Load())
	}
	if s.RedisFired.Load() > 0 {
		out += fmt.Sprintf(" · redis %d (%d err)", s.RedisFired.Load(), s.RedisErr.Load())
	}
	if s.ScenLoops.Load() > 0 {
		out += fmt.Sprintf(" · scenarios %d loops (%d step errs)", s.ScenLoops.Load(), s.ScenErr.Load())
	}
	return out
}
