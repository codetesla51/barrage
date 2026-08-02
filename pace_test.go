package barrage

import (
	"testing"
	"time"
)

func TestNextHitTimeConstantRate(t *testing.T) {
	if got := nextHitTime(1, 10, 0); got != 100*time.Millisecond {
		t.Errorf("nextHitTime(1, rate 10) = %v, want 100ms", got)
	}
	if got := nextHitTime(10, 10, 0); got != time.Second {
		t.Errorf("nextHitTime(10, rate 10) = %v, want 1s", got)
	}
	if got := nextHitTime(1, 100, 0); got != 10*time.Millisecond {
		t.Errorf("nextHitTime(1, rate 100) = %v, want 10ms", got)
	}
}

func TestNextHitTimeRamp(t *testing.T) {
	const ramp = 10 * time.Second
	// Ramp fires half the steady-rate hits (rate*ramp/2) by the end of the ramp.
	lastRampHit := nextHitTime(500, 100, ramp) // 100*10/2 = 500 hits during ramp
	if lastRampHit < 9*time.Second || lastRampHit > 11*time.Second {
		t.Errorf("hit 500 (end of ramp) at %v, want ~10s", lastRampHit)
	}
	firstSteady := nextHitTime(501, 100, ramp)
	if firstSteady <= lastRampHit || firstSteady-lastRampHit != 10*time.Millisecond {
		t.Errorf("hit 501 at %v, want 10ms after hit 500 (%v)", firstSteady, lastRampHit)
	}
	// The first hits are spaced out and speed up toward the steady interval.
	if nextHitTime(1, 100, ramp) >= nextHitTime(2, 100, ramp)-10*time.Millisecond {
		t.Error("expected the first interval to start long and shrink")
	}
}

func TestRateFor(t *testing.T) {
	const ramp = 10 * time.Second
	if got := rateFor(0, ramp, 100); got != 1 {
		t.Errorf("rateFor(0) = %d, want 1", got)
	}
	if got := rateFor(5*time.Second, ramp, 100); got != 50 {
		t.Errorf("rateFor(mid-ramp) = %d, want 50", got)
	}
	if got := rateFor(10*time.Second, ramp, 100); got != 100 {
		t.Errorf("rateFor(end-of-ramp) = %d, want 100", got)
	}
	if got := rateFor(20*time.Second, ramp, 100); got != 100 {
		t.Errorf("rateFor(after-ramp) = %d, want 100", got)
	}
	if got := rateFor(time.Second, 0, 100); got != 100 {
		t.Errorf("rateFor(no ramp) = %d, want 100", got)
	}
}

func TestRunPacedFiresAtRate(t *testing.T) {
	overall, start := runPaced(100, 5, 500*time.Millisecond, 0, func() dbQueryResult {
		return dbQueryResult{Latency: time.Millisecond, Success: true}
	})

	if start.IsZero() {
		t.Error("expected a non-zero run start time")
	}
	// rate 100/s over 0.5s → roughly 50 requests; allow generous slack.
	if len(overall) < 20 || len(overall) > 100 {
		t.Errorf("expected ~50 results, got %d", len(overall))
	}
	for i, res := range overall {
		if res.Timestamp.IsZero() {
			t.Errorf("result %d has a zero timestamp", i)
		}
		if !res.Success {
			t.Error("expected all fire tasks to succeed")
		}
	}
}

func TestRunPacedWithRamp(t *testing.T) {
	overall, _ := runPaced(200, 5, 500*time.Millisecond, 250*time.Millisecond, func() dbQueryResult {
		return dbQueryResult{Latency: time.Millisecond, Success: true}
	})

	// A ramp starts slow, so far fewer than the steady ~100 requests fire.
	if len(overall) >= 100 {
		t.Errorf("ramped run fired %d requests, expected well below the steady rate", len(overall))
	}
	if len(overall) == 0 {
		t.Error("expected at least one request even during a ramp")
	}
}
