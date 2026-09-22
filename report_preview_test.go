package barrage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// previewData is a hand-built run used only to eyeball the report template in a
// browser. It is not part of the shipped report path.
func previewData() ReportData {
	base := time.Unix(1700000000, 0)
	var spikes []CorrelatedSpike
	for i := 0; i < 7; i++ {
		runners := []string{"db", "redis"}
		spikes = append(spikes, CorrelatedSpike{
			BucketIndex:    base.Add(time.Duration(i*3) * time.Second).Unix(),
			Runner:         runners[i%2],
			HTTPLatency:    time.Duration(120+i*70) * time.Millisecond,
			StorageLatency: time.Duration(820+i*240) * time.Millisecond,
			Masked:         i == 2 || i == 5,
		})
	}
	steps := []CapacityStep{
		{Concurrency: 1, Requests: 900, P99: 22 * time.Millisecond, Success: 1.0},
		{Concurrency: 2, Requests: 1800, P99: 31 * time.Millisecond, Success: 1.0},
		{Concurrency: 4, Requests: 3400, P99: 48 * time.Millisecond, Success: 1.0},
		{Concurrency: 8, Requests: 6100, P99: 88 * time.Millisecond, Success: 1.0},
		{Concurrency: 12, Requests: 8800, P99: 146 * time.Millisecond, Success: 1.0},
		{Concurrency: 16, Requests: 9900, P99: 420 * time.Millisecond, Success: 0.72, Broken: true, BrokenBy: []string{"scenario"}, ScenarioErrs: map[string]uint64{"dial_timeout": 5120, "5xx": 1800}},
		{Concurrency: 24, Requests: 11000, P99: 980 * time.Millisecond, Success: 0.41, Broken: true, BrokenBy: []string{"scenario", "db"}, ScenarioErrs: map[string]uint64{"dial_timeout": 9100}},
		{Concurrency: 32, Requests: 12000, P99: 1480 * time.Millisecond, Success: 0.18, Broken: true, BrokenBy: []string{"db"}},
	}

	tl := TimelineChart{Labels: nil}
	for i := 0; i < 24; i++ {
		tl.Labels = append(tl.Labels, base.Add(time.Duration(i)*time.Second).Format("15:04:05"))
	}
	tl.Series = []TimelineSeries{
		{Name: "HTTP", P99: []int{11, 14, 18, 22, 26, 31, 38, 44, 52, 61, 70, 84, 96, 88, 74, 66, 58, 71, 92, 120, 155, 190, 240, 310}},
		{Name: "DB", P99: []int{4, 5, 5, 6, 7, 9, 11, 13, 16, 19, 24, 30, 38, 44, 36, 28, 21, 25, 33, 44, 61, 88, 130, 210}},
		{Name: "Redis", P99: []int{2, 2, 3, 3, -1, -1, 4, 5, 6, 8, 9, 12, 15, 18, 14, 11, 9, 10, 13, 17, 22, 29, 41, 63}},
	}

	return ReportData{
		CorrelationResult: CorrelationResult{Spikes: spikes},
		Runners: []RunnerSummary{
			{Name: "HTTP", Requests: 120400, Success: 99.7, P50: 9 * time.Millisecond, P95: 84 * time.Millisecond, P99: 246 * time.Millisecond, Max: 1480 * time.Millisecond, Mean: 21 * time.Millisecond, Rate: 4013.3, Throughput: 3980.1, StatusCodes: map[string]int{"200": 120040, "500": 360}},
			{Name: "DB", Requests: 96300, Success: 100, P50: 5 * time.Millisecond, P95: 62 * time.Millisecond, P99: 210 * time.Millisecond, Max: 990 * time.Millisecond, Mean: 14 * time.Millisecond, Rate: 3210, Throughput: 3190.4},
			{Name: "Redis", Requests: 41200, Success: 100, P50: 1 * time.Millisecond, P95: 9 * time.Millisecond, P99: 63 * time.Millisecond, Max: 180 * time.Millisecond, Mean: 3 * time.Millisecond, Rate: 1373.3, Throughput: 1370},
		},
		Timeline:    tl,
		Duration:    "30s",
		Ramp:        "5s",
		Concurrency: 32,
		CapacitySearch: &CapacityResult{
			BreakAt: 16,
			LastOK:  12,
			Steps:   steps,
		},
	}
}

// stressPreviewData is the worst case a real run can hand the template: a long
// spike list, a deep sweep, multi-word error classes and a runner name that
// does not fit anywhere. It exists so layout regressions are obvious.
func stressPreviewData(data ReportData) ReportData {
	base := time.Unix(1700000000, 0)
	runners := []string{"db", "redis", "postgres-primary-replica-2-eu-west-1-readonly"}

	spikes := make([]CorrelatedSpike, 0, 400)
	for i := 0; i < 400; i++ {
		latency := time.Duration(90+i%320) * time.Millisecond
		storage := time.Duration(600+(i*37)%5200) * time.Millisecond
		if i%7 == 0 {
			latency, storage = storage, latency // sometimes the runner wins instead
		}
		spikes = append(spikes, CorrelatedSpike{
			BucketIndex:    base.Add(time.Duration(i) * time.Second).Unix(),
			Runner:         runners[i%len(runners)],
			HTTPLatency:    latency,
			StorageLatency: storage,
			Masked:         i%11 == 0,
		})
	}
	data.Spikes = spikes

	steps := make([]CapacityStep, 0, 96)
	for i := 1; i <= 96; i++ {
		step := CapacityStep{
			Concurrency: i,
			Requests:    uint64(900 * i),
			P99:         time.Duration(18+i*13) * time.Millisecond,
			Success:     1.0,
		}
		if i >= 60 {
			step.Broken = true
			step.Success = 0.42
			step.BrokenBy = []string{"scenario", "db"}
			step.ScenarioErrs = map[string]uint64{
				"dial_timeout_exceeded_on_upstream_connection":         uint64(i) * 130,
				"unexpected_status_5xx_from_downstream_dependency_500": uint64(i) * 91,
			}
		}
		steps = append(steps, step)
	}
	data.CapacitySearch = &CapacityResult{BreakAt: 60, LastOK: 59, Steps: steps}

	data.Runners[0].Name = "postgres-primary-replica-2-eu-west-1-readonly"
	data.Runners[0].StatusCodes = map[string]int{
		"200": 120040, "204": 812, "301": 44, "400": 12, "429": 1180, "500": 360, "503": 21,
	}
	return data
}

// TestReportPreview is a dev-only helper, not an assertion of behavior: it
// renders five sample runs (full, single runner, two runners, no spikes, and a
// worst-case stress run) to /tmp/barrage-preview so the template can be
// eyeballed in a browser after a CSS or layout change. Run it with:
//
//	BARRAGE_PREVIEW_OUT=/tmp/barrage-preview/report.html go test -run TestReportPreview -v .
func TestReportPreview(t *testing.T) {
	out := os.Getenv("BARRAGE_PREVIEW_OUT")
	if out == "" {
		out = "/tmp/barrage-preview/report.html"
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"full", "one", "two", "nospikes", "stress"} {
		data := previewData()
		switch variant {
		case "one":
			data.Runners = data.Runners[:1]
			data.Spikes = nil
			data.CapacitySearch = nil
		case "two":
			data.Runners = data.Runners[:2]
		case "nospikes":
			data.Spikes = nil
			data.Timeline.Series = data.Timeline.Series[:1]
		case "stress":
			data = stressPreviewData(data)
		}
		var buf bytes.Buffer
		if err := RenderHTML(data, testTemplatePath, &buf); err != nil {
			t.Fatalf("render %s: %v", variant, err)
		}
		path := strings.TrimSuffix(out, ".html") + "-" + variant + ".html"
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PREVIEW %s: %s (%d bytes)\n", variant, path, buf.Len())
	}

	// Show the emitted timeline script so quoting of Go strings inside the JS
	// can be eyeballed.
	var buf bytes.Buffer
	if err := RenderHTML(previewData(), testTemplatePath, &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	i := strings.Index(html, "tDatasets.push")
	if i >= 0 {
		fmt.Printf("---- timeline script ----\n%s\n-------------------------\n", html[i:i+140])
	}
}
