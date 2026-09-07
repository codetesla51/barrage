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
	if !strings.Contains(out, "3 err") {
		t.Errorf("expected error count in view, got %q", out)
	}
}

func TestProgressViewNilStatsDoesNotPanic(t *testing.T) {
	m := NewProgressModel(nil, time.Minute)
	if got := m.View(); got != "" {
		t.Errorf("expected empty view for nil stats, got %q", got)
	}
}
