package barrage

// Benchmarks for barrage's own overhead — the cost of measuring, not the cost
// of the thing being measured.
//
// Why these exist: a load generator records every request inside the same
// process that issues it. Whatever this file costs is CPU stolen from the
// request being timed, so it shows up in the p99 barrage reports back. A
// benchmark that says "recording a sample costs 400ns" is saying "at 12k
// req/s that is 0.5% of a core" — which is either noise or the whole story,
// depending on the number.
//
// Run with -benchmem. Allocation count matters more than nanoseconds here:
// the recording paths retain results for the whole run, so a high allocs/op
// figure is a memory curve problem, not a CPU one.

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// makeResults builds n vegeta results spread over a window, the shape the
// HTTP recorder actually sees during a run.
func makeResults(n int, window time.Duration, seed int64) []*vegeta.Result {
	rng := rand.New(rand.NewSource(seed))
	base := time.Unix(1758000000, 0)
	out := make([]*vegeta.Result, n)
	for i := range out {
		latency := time.Duration(rng.Intn(2_000_000)) * time.Nanosecond
		code := 200
		switch rng.Intn(20) {
		case 0:
			code = 500
		case 1:
			code = 429
		}
		out[i] = &vegeta.Result{
			Code:      uint16(code),
			Timestamp: base.Add(time.Duration(i) * window / time.Duration(n)),
			Latency:   latency,
			Method:    "GET",
			URL:       "http://localhost:8080/api/products",
		}
	}
	return out
}

// BenchmarkRecordSample measures the per-request body of the HTTP loop: the
// atomic counter, vegeta's own metrics aggregation, and the bucket map
// append. This is the single hottest path in the tool — it runs once per
// request for the entire run.
//
// The sub-benchmarks separate the three costs so a regression points at a
// cause: map growth (retention), histogram update, or atomic contention.
func BenchmarkRecordSample(b *testing.B) {
	const width = time.Second

	// Results are spread over a fixed 60s window, not one per second, so the
	// bucket count matches a real run (one key per bucket_width) instead of
	// giving every sample its own map key and measuring cache misses.
	b.Run("bucket-map-append-only", func(b *testing.B) {
		b.ReportAllocs()
		bucketed := make(map[int64][]*vegeta.Result)
		results := makeResults(b.N, 60*time.Second, 1)
		b.ResetTimer()
		for _, r := range results {
			idx := r.Timestamp.UnixNano() / int64(width)
			bucketed[idx] = append(bucketed[idx], r)
		}
		b.StopTimer()
		b.ReportMetric(float64(len(bucketed)), "buckets")
	})

	b.Run("vegeta-metrics-add", func(b *testing.B) {
		b.ReportAllocs()
		var overall vegeta.Metrics
		results := makeResults(b.N, time.Duration(b.N)*time.Second, 2)
		b.ResetTimer()
		for _, r := range results {
			overall.Add(r)
		}
		overall.Close()
	})

	b.Run("atomic-counter", func(b *testing.B) {
		b.ReportAllocs()
		var stats RunStats
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			stats.HTTPFired.Add(1)
		}
	})
}

// BenchmarkBuildHTTPBuckets measures the end-of-run cost: sorting bucket
// indices and reducing every retained result into per-bucket percentiles.
// Cost grows with the number of requests held in memory, so this is where a
// long run pays for its retention.
func BenchmarkBuildHTTPBuckets(b *testing.B) {
	// Fixed 60s window: bucket count is set by bucket_width and run length,
	// not by request count. Spreading one result per second here would build
	// one HDR histogram per result (~17KB each) and report a fake O(n) memory
	// blowup that a real run never sees.
	for _, n := range []int{1_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("results=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			results := makeResults(n, 60*time.Second, 3)
			bucketed := make(map[int64][]*vegeta.Result)
			for _, r := range results {
				idx := r.Timestamp.UnixNano() / int64(time.Second)
				bucketed[idx] = append(bucketed[idx], r)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buildHTTPBuckets(bucketed, time.Second)
			}
		})
	}
}

// makeScenarioResults builds n scenario iterations with a realistic step
// count, matching what a multi-step journey leaves behind.
func makeScenarioResults(n, steps int, seed int64) []ScenarioResult {
	rng := rand.New(rand.NewSource(seed))
	base := time.Unix(1758000000, 0)
	names := []string{"browse", "checkout-flow", "login-only", "health-ping"}
	out := make([]ScenarioResult, n)
	for i := range out {
		sr := make([]StepResult, steps)
		for j := range sr {
			sr[j] = StepResult{
				Method:     "GET",
				URL:        "http://localhost:8080/api/products",
				StatusCode: 200,
				Duration:   time.Duration(rng.Intn(500_000)) * time.Nanosecond,
			}
		}
		out[i] = ScenarioResult{
			UserID:       i % 300,
			ScenarioName: names[i%len(names)],
			Start:        base.Add(time.Duration(i) * 60 * time.Second / time.Duration(n)),
			Duration:     time.Duration(rng.Intn(50_000_000)) * time.Nanosecond,
			Steps:        sr,
		}
	}
	return out
}

// BenchmarkBuildScenarioStats measures the scenario aggregation path: bucket
// grouping plus the full latency sort behind P50/P95/P99. The sort is
// O(n log n) over every iteration in the run, so it is the cost most likely
// to surprise someone running a long soak.
func BenchmarkBuildScenarioStats(b *testing.B) {
	runStart := time.Unix(1758000000, 0)
	for _, n := range []int{1_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("iterations=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			results := makeScenarioResults(n, 4, 4)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buildScenarioStats(results, runStart, time.Second, time.Minute)
			}
		})
	}
}

// BenchmarkBuildScenarioStatsByName covers the multi-scenario grouping used
// by scenario mode, where every iteration is bucketed by journey name before
// aggregation.
func BenchmarkBuildScenarioStatsByName(b *testing.B) {
	runStart := time.Unix(1758000000, 0)
	results := makeScenarioResults(100_000, 4, 5)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildScenarioStatsByName(results, runStart, time.Second, time.Minute)
	}
}

// BenchmarkInterpolate measures variable substitution, which runs once per
// step in any journey that extracts a token. The no-variable case is the
// fast path every other step takes; both are measured so the cost of
// adding an extract to a step is visible.
func BenchmarkInterpolate(b *testing.B) {
	vars := map[string]any{"token": "eyJhbGciOiJIUzI1NiJ9.payload.signature"}
	cases := []struct {
		name string
		in   string
		vars map[string]any
	}{
		{"no-vars-fast-path", "http://localhost:8080/api/products", nil},
		{"no-vars-nonempty", "http://localhost:8080/api/products", vars},
		{"one-var-url", "http://localhost:8080/api/checkout?token={{token}}", vars},
		{"one-var-body", `{"amount":1,"t":"{{token}}"}`, vars},
		{"missing-var", "http://localhost:8080/api/me?t={{nope}}", vars},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = interpolate(c.in, c.vars)
			}
		})
	}
}

// makeDBResults builds n query results spread over a fixed 60s window, the
// shape runPaced hands to buildDBResult. Both db: and redis: runners share
// this aggregation path (redis.go calls buildDBResult), so these benchmarks
// cover both.
func makeDBResults(n int, failEvery int) []dbQueryResult {
	base := time.Unix(1758000000, 0)
	out := make([]dbQueryResult, n)
	for i := range out {
		var err error
		if failEvery > 0 && i%failEvery == 0 {
			err = errors.New("connection refused")
		}
		out[i] = dbQueryResult{
			Timestamp: base.Add(time.Duration(i) * 60 * time.Second / time.Duration(n)),
			Latency:   time.Duration(i%2_000_000) * time.Nanosecond,
			Success:   err == nil,
			Err:       err,
		}
	}
	return out
}

// BenchmarkRecordQueryContention measures the db:/redis: record path — a
// single mutex guarding one shared append, hit by every pool worker. HTTP
// does not share this shape (vegeta owns its own metrics), so this is the
// only place a load generator's own bookkeeping can serialise workers.
//
// The sub-benchmark contrast is the point: "locked" pays the mutex, "unlocked"
// shows the floor. The gap is what the lock costs at that worker count.
func BenchmarkRecordQueryContention(b *testing.B) {
	for _, workers := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("workers=%d/locked", workers), func(b *testing.B) {
			b.ReportAllocs()
			overall := make([]dbQueryResult, 0)
			var mu sync.Mutex
			var wg sync.WaitGroup
			per := b.N / workers
			if per < 1 {
				per = 1
			}
			b.ResetTimer()
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res := dbQueryResult{Latency: time.Millisecond, Success: true}
					for i := 0; i < per; i++ {
						mu.Lock()
						overall = append(overall, res)
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			b.StopTimer()
			b.ReportMetric(float64(workers), "workers")
		})

		b.Run(fmt.Sprintf("workers=%d/unlocked-floor", workers), func(b *testing.B) {
			b.ReportAllocs()
			overall := make([]dbQueryResult, 0, b.N)
			var wg sync.WaitGroup
			per := b.N / workers
			if per < 1 {
				per = 1
			}
			b.ResetTimer()
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					res := dbQueryResult{Latency: time.Millisecond, Success: true}
					local := make([]dbQueryResult, 0, per)
					for i := 0; i < per; i++ {
						local = append(local, res)
					}
					overall = append(overall, local...)
				}()
			}
			wg.Wait()
		})
	}
}

// BenchmarkBuildDBResult measures end-of-run aggregation for db:/redis::
// bucket grouping plus the latency sort behind P50/P95/P99. The 60s window
// keeps bucket count tied to bucket_width, as in BenchmarkBuildHTTPBuckets.
func BenchmarkBuildDBResult(b *testing.B) {
	runStart := time.Unix(1758000000, 0)
	for _, n := range []int{1_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("queries=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			overall := makeDBResults(n, 50)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = buildDBResult(overall, runStart, time.Second, time.Minute)
			}
		})
	}
}

// BenchmarkChaosWindows measures pairing fault events into report windows.
// Cheap in absolute terms, but it runs over the event log on every report
// render, and a long run with many faults grows that log linearly.
func BenchmarkChaosWindows(b *testing.B) {
	base := time.Unix(1758000000, 0)
	for _, pairs := range []int{10, 500, 5_000} {
		b.Run(fmt.Sprintf("windows=%d", pairs), func(b *testing.B) {
			b.ReportAllocs()
			events := make([]ChaosEvent, 0, pairs*2)
			for i := 0; i < pairs; i++ {
				off := time.Duration(i*2) * time.Second
				events = append(events,
					ChaosEvent{Offset: off, At: base.Add(off), Proxy: "db-proxy", Toxic: "latency_downstream", Type: "latency", Action: "add"},
					ChaosEvent{Offset: off + time.Second, At: base.Add(off + time.Second), Proxy: "db-proxy", Toxic: "latency_downstream", Type: "latency", Action: "remove"},
				)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ChaosWindows(events)
			}
		})
	}
}
