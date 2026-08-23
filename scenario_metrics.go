package barrage

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ScenarioStats holds aggregated metrics for a scenario run.
// Requests counts scenario iterations (not individual HTTP steps).
// Latencies are total scenario duration (all steps) per iteration.
type ScenarioStats struct {
	Requests   uint64
	Success    float64
	Errors     []string
	P50        time.Duration
	P95        time.Duration
	P99        time.Duration
	Max        time.Duration
	Mean       time.Duration
	Rate       float64
	Throughput float64
	Earliest   time.Time
	Latest     time.Time
	Buckets    []Bucket
}

// NamedScenarioStats pairs a scenario name with its aggregated stats.
type NamedScenarioStats struct {
	Name  string
	Stats *ScenarioStats
}

// effectiveScenarios returns the configured scenarios with defaults.
func effectiveScenarios(cfg OrchestratorConfig) []Scenario {
	out := make([]Scenario, 0, len(cfg.Scenarios))
	for _, s := range cfg.Scenarios {
		if s.Weight == 0 {
			s.Weight = 1
		}
		if s.Name == "" {
			s.Name = "scenario"
		}
		out = append(out, s)
	}
	return out
}

// FireScenario runs a scenario and returns aggregated metrics.
// It is the scenario equivalent of FireHTTP / FireDB.
func FireScenario(s Scenario, concurrency int, duration, bucketWidth time.Duration, prog ...*RunProgress) (*ScenarioStats, error) {
	if len(s.Steps) == 0 {
		return nil, nil
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if bucketWidth <= 0 {
		bucketWidth = time.Second
	}
	if s.Name == "" {
		s.Name = "scenario"
	}

	runner := NewHTTPRunner(nil)
	runStart := time.Now()

	cfg := OrchestratorConfig{
		Duration:    Duration(duration),
		Concurrency: concurrency,
	}

	results := RunScenario(context.Background(), s, cfg, runner)
	recordScenarioProgress(liveProg(prog), s.Name, results)
	stats := buildScenarioStats(results, runStart, bucketWidth, duration)
	return stats, nil
}

// FireScenarios runs multiple weighted scenarios and returns per-name stats.
func FireScenarios(scenarios []Scenario, concurrency int, duration, bucketWidth time.Duration, prog ...*RunProgress) ([]NamedScenarioStats, error) {
	if len(scenarios) == 0 {
		return nil, nil
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if bucketWidth <= 0 {
		bucketWidth = time.Second
	}
	for i := range scenarios {
		if scenarios[i].Weight == 0 {
			scenarios[i].Weight = 1
		}
		if scenarios[i].Name == "" {
			scenarios[i].Name = fmt.Sprintf("scenario-%d", i+1)
		}
	}

	runner := NewHTTPRunner(nil)
	runStart := time.Now()

	cfg := OrchestratorConfig{
		Duration:    Duration(duration),
		Concurrency: concurrency,
	}

	results := RunScenarios(context.Background(), scenarios, cfg, runner)
	recordScenarioProgress(liveProg(prog), "", results)
	byName := buildScenarioStatsByName(results, runStart, bucketWidth, duration)

	ordered := make([]NamedScenarioStats, 0, len(byName))
	seen := make(map[string]bool)
	for _, s := range scenarios {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		if st, ok := byName[s.Name]; ok {
			ordered = append(ordered, NamedScenarioStats{Name: s.Name, Stats: st})
		}
	}
	for name, st := range byName {
		if !seen[name] {
			ordered = append(ordered, NamedScenarioStats{Name: name, Stats: st})
		}
	}
	return ordered, nil
}

// buildScenarioStatsByName groups results by ScenarioName and builds stats per group.
func buildScenarioStatsByName(results []ScenarioResult, runStart time.Time, bucketWidth, duration time.Duration) map[string]*ScenarioStats {
	if len(results) == 0 {
		return nil
	}
	grouped := make(map[string][]ScenarioResult)
	for _, r := range results {
		name := r.ScenarioName
		if name == "" {
			name = "scenario"
		}
		grouped[name] = append(grouped[name], r)
	}
	out := make(map[string]*ScenarioStats, len(grouped))
	for name, group := range grouped {
		out[name] = buildScenarioStats(group, runStart, bucketWidth, duration)
	}
	return out
}

// isScenarioSuccess reports whether a scenario iteration succeeded.
// Success means every step had no error and a 2xx status.
func isScenarioSuccess(sr ScenarioResult) bool {
	if len(sr.Steps) == 0 {
		return false
	}
	for _, st := range sr.Steps {
		if st.Err != nil {
			return false
		}
		if st.StatusCode < 200 || st.StatusCode >= 300 {
			return false
		}
	}
	return true
}

// buildScenarioStats aggregates per-iteration results into a ScenarioStats.
// It buckets by Start.Unix() truncated to bucketWidth, same as HTTP/DB.
func buildScenarioStats(results []ScenarioResult, runStart time.Time, bucketWidth, duration time.Duration) *ScenarioStats {
	stats := &ScenarioStats{
		Requests: uint64(len(results)),
		Earliest: runStart,
		Latest:   runStart.Add(duration),
		Buckets:  buildScenarioBuckets(results, bucketWidth),
	}

	if len(results) == 0 {
		return stats
	}

	latencies := make([]time.Duration, 0, len(results))
	var successN uint64
	var sum time.Duration
	errSeen := make(map[string]bool)

	for _, sr := range results {
		latencies = append(latencies, sr.Duration)
		sum += sr.Duration
		if isScenarioSuccess(sr) {
			successN++
		} else {
			// collect first error from failed steps
			for _, st := range sr.Steps {
				if st.Err != nil {
					msg := st.Err.Error()
					if !errSeen[msg] {
						errSeen[msg] = true
						stats.Errors = append(stats.Errors, msg)
					}
					break
				}
				if st.StatusCode < 200 || st.StatusCode >= 300 {
					msg := fmt.Sprintf("%s %s: status %d", st.Method, st.URL, st.StatusCode)
					if !errSeen[msg] {
						errSeen[msg] = true
						stats.Errors = append(stats.Errors, msg)
					}
					break
				}
			}
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	stats.Success = float64(successN) / float64(len(results))
	stats.Mean = sum / time.Duration(len(results))
	stats.Max = latencies[len(latencies)-1]
	stats.P50 = percentile(latencies, 0.50)
	stats.P95 = percentile(latencies, 0.95)
	stats.P99 = percentile(latencies, 0.99)

	secs := duration.Seconds()
	if secs > 0 {
		stats.Rate = float64(len(results)) / secs
		stats.Throughput = float64(successN) / secs
	}

	return stats
}

// buildScenarioBuckets groups scenario iterations into time buckets.
func buildScenarioBuckets(results []ScenarioResult, bucketWidth time.Duration) []Bucket {
	if len(results) == 0 || bucketWidth <= 0 {
		return nil
	}

	type bucketAgg struct {
		bucket    Bucket
		latencies []time.Duration
		successN  uint64
	}

	aggs := make(map[int64]*bucketAgg)

	for _, sr := range results {
		idx := sr.Start.Unix() / int64(bucketWidth.Seconds())
		a, ok := aggs[idx]
		if !ok {
			start := time.Unix(idx*int64(bucketWidth.Seconds()), 0)
			a = &bucketAgg{
				bucket: Bucket{
					Start: start.Unix(),
					End:   start.Add(bucketWidth).Unix(),
				},
			}
			aggs[idx] = a
		}
		a.bucket.Requests++
		if isScenarioSuccess(sr) {
			a.successN++
		}
		a.latencies = append(a.latencies, sr.Duration)
	}

	indices := make([]int64, 0, len(aggs))
	for idx := range aggs {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })

	buckets := make([]Bucket, 0, len(indices))
	for _, idx := range indices {
		a := aggs[idx]
		sort.Slice(a.latencies, func(i, j int) bool { return a.latencies[i] < a.latencies[j] })
		a.bucket.P50 = percentile(a.latencies, 0.50)
		a.bucket.P99 = percentile(a.latencies, 0.99)
		a.bucket.Success = time.Duration(a.successN)
		buckets = append(buckets, a.bucket)
	}
	return buckets
}


