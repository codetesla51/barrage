package barrage

import (
	"strings"
	"testing"
	"time"
)

func storyRunner(name string, p99 time.Duration, success float64) RunnerSummary {
	return RunnerSummary{Name: name, Requests: 100, Success: success, P99: p99, Rate: 10, Throughput: 10}
}

func TestBuildStoryVerdicts(t *testing.T) {
	cases := []struct {
		name      string
		data      ReportData
		wantTitle string
		wantTone  string
	}{
		{
			"failed run",
			ReportData{Error: "connection refused"},
			"Run failed before it could produce results", "err",
		},
		{
			"no runners",
			ReportData{},
			"No data — no runners returned results", "err",
		},
		{
			"clean fast",
			ReportData{Duration: "15s", Runners: []RunnerSummary{storyRunner("HTTP", 20*time.Millisecond, 100)}},
			"All good — fast and clean", "ok",
		},
		{
			"warm no spikes",
			ReportData{Duration: "15s", Runners: []RunnerSummary{storyRunner("HTTP", 150*time.Millisecond, 100)}},
			"Healthy but a bit warm", "ok",
		},
		{
			"errors without spikes",
			ReportData{Duration: "15s", Runners: []RunnerSummary{storyRunner("DB", 10*time.Millisecond, 80)}},
			"DB is failing", "err",
		},
		{
			"masked storage spike",
			ReportData{
				Duration: "15s",
				Runners: []RunnerSummary{
					storyRunner("HTTP", 20*time.Millisecond, 100),
					storyRunner("DB", 200*time.Millisecond, 100),
				},
				CorrelationResult: CorrelationResult{Spikes: []CorrelatedSpike{
					{BucketIndex: 1, Runner: "db", HTTPLatency: 20 * time.Millisecond, StorageLatency: 200 * time.Millisecond, Masked: true},
				}},
			},
			"Storage hiccup — app held", "ok",
		},
		{
			"correlated spike names bottleneck",
			ReportData{
				Duration: "15s",
				Runners: []RunnerSummary{
					storyRunner("HTTP", 150*time.Millisecond, 100),
					storyRunner("DB", 250*time.Millisecond, 100),
				},
				CorrelationResult: CorrelationResult{Spikes: []CorrelatedSpike{
					{BucketIndex: 1, Runner: "db", HTTPLatency: 150 * time.Millisecond, StorageLatency: 250 * time.Millisecond},
				}},
			},
			"Bottleneck: DB was the slowest", "err",
		},
		{
			"capacity broke",
			ReportData{CapacitySearch: &CapacityResult{
				Steps:   []CapacityStep{{Concurrency: 10, P99: 50 * time.Millisecond, Success: 1}, {Concurrency: 20, P99: 200 * time.Millisecond, Success: 1, Broken: true, BrokenBy: []string{"db"}}},
				BreakAt: 20, LastOK: 10,
			}},
			"Broke at 20 concurrent users", "err",
		},
		{
			"capacity held",
			ReportData{CapacitySearch: &CapacityResult{
				Steps:  []CapacityStep{{Concurrency: 10, P99: 50 * time.Millisecond, Success: 1}},
				LastOK: 10,
			}},
			"Held to 10 concurrent users", "ok",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildStory(c.data)
			if got.Title != c.wantTitle {
				t.Errorf("title = %q, want %q", got.Title, c.wantTitle)
			}
			if got.Tone != c.wantTone {
				t.Errorf("tone = %q, want %q", got.Tone, c.wantTone)
			}
			if got.Detail == "" {
				t.Error("expected non-empty detail")
			}
			if len(got.NextSteps) == 0 {
				t.Error("expected at least one next step")
			}
		})
	}
}

func TestCapacityLine(t *testing.T) {
	labels := make([]string, 12)
	for i := range labels {
		labels[i] = "t"
	}
	flat := make([]int, 12)
	for i := range flat {
		flat[i] = 20
	}
	spiked := make([]int, 12)
	for i := range spiked {
		spiked[i] = 20
	}
	spiked[8], spiked[9], spiked[10] = 200, 210, 220

	clean := BuildStory(ReportData{
		Concurrency: 50,
		Timeline:    TimelineChart{Labels: labels, Series: []TimelineSeries{{Name: "checkout", P99: flat}}},
		Runners:     []RunnerSummary{storyRunner("checkout", 20*time.Millisecond, 100)},
	})
	if !strings.HasPrefix(clean.Capacity, "no strain up to ~50 users") {
		t.Errorf("clean capacity = %q", clean.Capacity)
	}

	straining := BuildStory(ReportData{
		Concurrency: 50,
		Timeline:    TimelineChart{Labels: labels, Series: []TimelineSeries{{Name: "checkout", P99: spiked}}},
		Runners:     []RunnerSummary{storyRunner("checkout", 200*time.Millisecond, 100)},
	})
	if !strings.HasPrefix(straining.Capacity, "straining at ~50 users") {
		t.Errorf("straining capacity = %q", straining.Capacity)
	}

	infraOnly := BuildStory(ReportData{
		Concurrency: 50,
		Timeline:    TimelineChart{Labels: labels, Series: []TimelineSeries{{Name: "DB", P99: spiked}}},
		Runners:     []RunnerSummary{storyRunner("DB", 200*time.Millisecond, 100)},
	})
	if infraOnly.Capacity != "" {
		t.Errorf("infra-only capacity should be empty, got %q", infraOnly.Capacity)
	}
}
