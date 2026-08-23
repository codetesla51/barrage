package barrage

import (
	"sort"
	"sync"
	"time"
)

// RunProgress is a lightweight live view of a running load test. Runners
// record each completed hit into it as it happens; the CLI samples Snapshot()
// periodically to render inline progress. It is display-only — final reports
// keep using the exact per-runner metrics, so this never affects results.
type RunProgress struct {
	mu         sync.Mutex
	started    time.Time
	duration   time.Duration
	ramp       time.Duration
	concurrency int
	runners    map[string]*liveRunner
}

type liveRunner struct {
	reqs    uint64
	ok      uint64
	latSum  time.Duration
	max     time.Duration
	recent  []time.Duration // capped latency sample for approximate percentiles
}

const recentCap = 2048

// NewRunProgress creates a progress collector for a run with the given total
// duration and ramp.
func NewRunProgress(duration, ramp time.Duration, concurrency int) *RunProgress {
	return &RunProgress{
		started:     time.Now(),
		duration:    duration,
		ramp:        ramp,
		concurrency: concurrency,
		runners:     make(map[string]*liveRunner),
	}
}

// Record notes one completed hit for a runner. Safe for concurrent use.
func (p *RunProgress) Record(runner string, ok bool, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.runners[runner]
	if r == nil {
		r = &liveRunner{}
		p.runners[runner] = r
	}
	r.reqs++
	if ok {
		r.ok++
	}
	r.latSum += latency
	if latency > r.max {
		r.max = latency
	}
	r.recent = append(r.recent, latency)
	if len(r.recent) > recentCap {
		// drop the oldest half so the cap check stays amortized O(1)
		r.recent = append([]time.Duration(nil), r.recent[recentCap/2:]...)
	}
}

// RunnerLive is one runner's row in a live snapshot.
type RunnerLive struct {
	Name       string
	Requests   uint64
	SuccessPct float64
	P50        time.Duration
	P99        time.Duration
	Mean       time.Duration
	Rate       float64
}

// LiveSnapshot is a point-in-time view of run progress.
type LiveSnapshot struct {
	Elapsed     time.Duration
	Duration    time.Duration
	Ramp        time.Duration
	State       string // starting | ramping | running | done
	Concurrency int
	Runners     []RunnerLive
}

// state returns the run phase implied by elapsed time and recorded hits.
func (p *RunProgress) state(elapsed time.Duration, totalReqs uint64) string {
	switch {
	case totalReqs == 0 && elapsed < 2*time.Second:
		return "starting"
	case p.ramp > 0 && elapsed < p.ramp:
		return "ramping"
	default:
		return "running"
	}
}

// Snapshot renders the current live state. Percentiles are approximate — they
// are computed from the most recent latency samples, which is exactly what a
// live view wants.
func (p *RunProgress) Snapshot() LiveSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	snap := LiveSnapshot{
		Elapsed:     time.Since(p.started),
		Duration:    p.duration,
		Ramp:        p.ramp,
		Concurrency: p.concurrency,
	}
	var total uint64
	names := make([]string, 0, len(p.runners))
	for name := range p.runners {
		names = append(names, name)
	}
	sort.Strings(names)

	secs := snap.Elapsed.Seconds()
	if secs < 1 {
		secs = 1
	}
	for _, name := range names {
		r := p.runners[name]
		total += r.reqs
		lat := append([]time.Duration(nil), r.recent...)
		sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
		snap.Runners = append(snap.Runners, RunnerLive{
			Name:       name,
			Requests:   r.reqs,
			SuccessPct: float64(r.ok) / float64(r.reqs) * 100,
			P50:        percentile(lat, 0.50),
			P99:        percentile(lat, 0.99),
			Mean:       time.Duration(int64(r.latSum) / int64(r.reqs)),
			Rate:       float64(r.reqs) / secs,
		})
	}
	snap.State = p.state(snap.Elapsed, total)
	if snap.Elapsed > snap.Duration {
		snap.Elapsed = snap.Duration
		snap.State = "done"
	}
	return snap
}


// liveProg returns the optional progress collector from a variadic param, or
// nil when the caller did not pass one.
func liveProg(prog []*RunProgress) *RunProgress {
	if len(prog) == 0 {
		return nil
	}
	return prog[0]
}

// recordScenarioProgress feeds scenario iterations into the live collector.
// Each ScenarioResult is one virtual user completing one loop of the journey;
// the recorded latency is the mean of its step latencies. A name of "" uses
// each result's own ScenarioName (weighted multi-scenario runs).
func recordScenarioProgress(prog *RunProgress, name string, results []ScenarioResult) {
	if prog == nil {
		return
	}
	for _, r := range results {
		who := name
		if who == "" {
			who = r.ScenarioName
		}
		if who == "" {
			who = "scenario"
		}
		if len(r.Steps) == 0 || r.Duration <= 0 {
			continue
		}
		var sum time.Duration
		failed := 0
		for _, st := range r.Steps {
			sum += st.Duration
			if st.Err != nil {
				failed++
			}
		}
		mean := sum / time.Duration(len(r.Steps))
		prog.Record(who, failed == 0, mean)
	}
}
