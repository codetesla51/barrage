package barrage

import (
	"strings"
	"testing"
	"time"
)

func TestProgressViewRendersCounters(t *testing.T) {
	s := &RunStats{}
	s.HTTPFired.Store(3900)
	s.HTTPErr.Store(3)
	m := NewProgressModel(s, 3*time.Minute)
	out := m.View()
	if !strings.Contains(out, "http") {
		t.Errorf("expected runner name in view, got %q", out)
	}
	if !strings.Contains(out, "3,900") {
		t.Errorf("expected comma-grouped count in view, got %q", out)
	}
}

// Errors are per-tick deltas: the first sample only records a baseline, so
// the view needs one tick before an error burst shows. Drive it through
// Update so the test covers the same path the program uses.
func TestProgressViewShowsErrorDeltaAfterTick(t *testing.T) {
	s := &RunStats{}
	s.HTTPFired.Store(3900)
	s.HTTPErr.Store(1)
	m := NewProgressModel(s, 3*time.Minute)
	_, _ = m.Update(progressTickMsg(time.Now()))
	s.HTTPErr.Store(4) // three new errors since the baseline
	_, _ = m.Update(progressTickMsg(time.Now().Add(250 * time.Millisecond)))
	out := m.View()
	if !strings.Contains(out, "3 err") {
		t.Errorf("expected per-tick error delta in view, got %q", out)
	}
}

// Only runners that fired get a lane — an http+redis run must not render
// dead db/scen rows.
func TestProgressViewOmitsIdleRunners(t *testing.T) {
	s := &RunStats{}
	s.HTTPFired.Store(10)
	m := NewProgressModel(s, time.Minute)
	out := m.View()
	for _, idle := range []string{"db", "redis", "scen"} {
		if strings.Contains(out, idle) {
			t.Errorf("expected no %q lane when idle, got %q", idle, out)
		}
	}
}

// The view must end with exactly one newline: tea's renderer moves up
// lines-1 to repaint, so a missing or doubled trailing newline makes the
// block creep down the screen a line every tick.
func TestProgressViewEndsWithSingleNewline(t *testing.T) {
	s := &RunStats{}
	s.HTTPFired.Store(10)
	s.DBFired.Store(4)
	out := NewProgressModel(s, time.Minute).View()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("view must end with a newline, got %q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Errorf("view must end with exactly one newline, got %q", out)
	}
	if n := strings.Count(out, "\n"); n != 3 { // clock + 2 lanes
		t.Errorf("expected 3 lines for 2 active runners, got %d (%q)", n, out)
	}
}
func TestTrailLenLogScale(t *testing.T) {
	cases := []struct {
		rate float64
		want int
	}{
		{0, 1},
		{-5, 1},     // must never produce a negative trail
		{20, 4},     // slow db
		{290, 7},    // fast cache
		{100000, 7}, // clamped
	}
	for _, c := range cases {
		if got := trailLen(c.rate); got != c.want {
			t.Errorf("trailLen(%v) = %d, want %d", c.rate, got, c.want)
		}
	}
}

func TestProgressViewNilStatsDoesNotPanic(t *testing.T) {
	m := NewProgressModel(nil, time.Minute)
	if got := m.View(); got != "" {
		t.Errorf("expected empty view for nil stats, got %q", got)
	}
}
