package barrage

import (
	"math"
	"sync"
	"time"

	"github.com/alitto/pond/v2"
)

// DefaultConcurrency is the worker count used when a config leaves concurrency
// at zero. It is enough to keep up with typical local rates while still firing
// several requests in parallel.
const DefaultConcurrency = 10

// nextHitTime returns the target time (measured from the run start) at which
// hit number hits (1-based) should fire, for a rate that ramps linearly from 0
// to rate over the ramp period and then holds. Without a ramp it schedules a
// fixed interval.
func nextHitTime(hits uint64, rate int, ramp time.Duration) time.Duration {
	if rate <= 0 {
		return 0
	}
	if ramp <= 0 {
		return time.Duration(int64(time.Second)/int64(rate)) * time.Duration(hits)
	}
	rampSeconds := float64(ramp) / float64(time.Second)
	rampHits := float64(rate) * rampSeconds / 2.0 // hits fired by the end of the ramp
	var seconds float64
	if float64(hits) <= rampHits {
		seconds = math.Sqrt(2.0 * rampSeconds * float64(hits) / float64(rate))
	} else {
		seconds = rampSeconds + (float64(hits)-rampHits)/float64(rate)
	}
	return time.Duration(seconds * float64(time.Second))
}

// rateFor returns the request rate (per second) in effect at elapsed time when
// ramping linearly from 0 to rate over the ramp period.
func rateFor(elapsed, ramp time.Duration, rate int) int {
	if ramp > 0 && elapsed < ramp {
		r := float64(rate) * float64(elapsed) / float64(ramp)
		if r < 1 {
			return 1
		}
		return int(r)
	}
	return rate
}

// runPaced fires fn at the given rate for the duration, using a worker pool
// capped at concurrency workers. It returns the collected results plus the
// time the run started, which callers use to align buckets. Results are
// recorded with the time they were submitted so buckets reflect load timing,
// not completion timing.
func runPaced(rate, concurrency int, duration, ramp time.Duration, fn func() dbQueryResult) ([]dbQueryResult, time.Time) {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	pool := pond.NewPool(concurrency)

	var mu sync.Mutex
	overall := make([]dbQueryResult, 0)
	start := time.Now()
	next := time.NewTimer(0)
	defer next.Stop()
	deadline := time.After(duration)
	hits := uint64(0)

	for {
		select {
		case <-next.C:
			submitted := time.Now()
			hits++
			pool.Submit(func() {
				res := fn()
				res.Timestamp = submitted
				mu.Lock()
				overall = append(overall, res)
				mu.Unlock()
			})
			wait := nextHitTime(hits+1, rate, ramp) - submitted.Sub(start)
			if wait < 0 {
				wait = 0
			}
			next.Reset(wait)
		case <-deadline:
			pool.StopAndWait()
			return overall, start
		}
	}
}
