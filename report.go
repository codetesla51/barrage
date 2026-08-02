package barrage

import (
	"fmt"
	"html/template"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// RunnerSummary holds the aggregate results for a single runner, formatted for
// the report's run-summary section.
type RunnerSummary struct {
	Name        string
	Requests    uint64
	Success     float64 // percent, 0-100
	P50         time.Duration
	P95         time.Duration
	P99         time.Duration
	Max         time.Duration
	Mean        time.Duration
	Rate        float64
	Throughput  float64
	StatusCodes map[string]int
}

// TimelineSeries is one runner's per-bucket P99 latency over the whole run,
// aligned to TimelineChart.Labels.
type TimelineSeries struct {
	Name string
	P99  []int // milliseconds; -1 = no request in that bucket
}

// TimelineChart is a full-run latency timeline: every bucket across all
// runners, aligned to a shared set of labels.
type TimelineChart struct {
	Labels []string
	Series []TimelineSeries
}

// ReportData is the full data model for the HTML report: the correlated-spike
// analysis, one RunnerSummary per runner that ran, and a full-run latency
// timeline. Duration, Ramp, and Concurrency describe the run that produced it
// and are included in the JSON export.
type ReportData struct {
	CorrelationResult
	Runners     []RunnerSummary
	Timeline    TimelineChart
	Duration    string
	Ramp        string
	Concurrency int
}

// NewReportData assembles a ReportData from an OrchestratorResult and the
// correlation analysis. Runners that did not run are omitted.
func NewReportData(result *OrchestratorResult, correlation CorrelationResult) ReportData {
	data := ReportData{CorrelationResult: correlation, Timeline: buildTimeline(result)}
	if result == nil {
		return data
	}
	if result.HTTPResult != nil {
		data.Runners = append(data.Runners, summarizeRunner("HTTP", result.HTTPResult.Requests, result.HTTPResult.Success, result.HTTPResult.P50, result.HTTPResult.P95, result.HTTPResult.P99, result.HTTPResult.Max, result.HTTPResult.Mean, result.HTTPResult.Rate, result.HTTPResult.Throughput, result.HTTPResult.StatusCodes))
	}
	if result.DBResult != nil {
		data.Runners = append(data.Runners, summarizeRunner("DB", result.DBResult.Requests, result.DBResult.Success, result.DBResult.P50, result.DBResult.P95, result.DBResult.P99, result.DBResult.Max, result.DBResult.Mean, result.DBResult.Rate, result.DBResult.Throughput, nil))
	}
	if result.RedisResult != nil {
		data.Runners = append(data.Runners, summarizeRunner("Redis", result.RedisResult.Requests, result.RedisResult.Success, result.RedisResult.P50, result.RedisResult.P95, result.RedisResult.P99, result.RedisResult.Max, result.RedisResult.Mean, result.RedisResult.Rate, result.RedisResult.Throughput, nil))
	}
	return data
}

func summarizeRunner(name string, requests uint64, success float64, p50, p95, p99, max, mean time.Duration, rate, throughput float64, statusCodes map[string]int) RunnerSummary {
	return RunnerSummary{
		Name:        name,
		Requests:    requests,
		Success:     success * 100,
		P50:         p50,
		P95:         p95,
		P99:         p99,
		Max:         max,
		Mean:        mean,
		Rate:        rate,
		Throughput:  throughput,
		StatusCodes: statusCodes,
	}
}

// buildTimeline aligns every runner's per-bucket P99 latency onto a shared set
// of bucket labels (bucket index = unix second). Buckets present on only one
// runner are kept; missing values are marked -1 so the chart can render them
// as gaps.
func buildTimeline(result *OrchestratorResult) TimelineChart {
	var chart TimelineChart
	if result == nil {
		return chart
	}

	type series struct {
		name string
		p99  map[int64]time.Duration
	}
	var seriesList []series
	indexSet := make(map[int64]bool)

	addSeries := func(name string, buckets []Bucket) {
		if len(buckets) == 0 {
			return
		}
		m := make(map[int64]time.Duration, len(buckets))
		for _, b := range buckets {
			m[b.Start] = b.P99
			indexSet[b.Start] = true
		}
		seriesList = append(seriesList, series{name: name, p99: m})
	}

	if result.HTTPResult != nil {
		http := make([]Bucket, len(result.HTTPResult.Buckets))
		for i, b := range result.HTTPResult.Buckets {
			http[i] = Bucket{Start: b.Start.Unix(), P99: b.P99}
		}
		addSeries("HTTP", http)
	}
	if result.DBResult != nil {
		addSeries("DB", result.DBResult.Buckets)
	}
	if result.RedisResult != nil {
		addSeries("Redis", result.RedisResult.Buckets)
	}

	indices := make([]int64, 0, len(indexSet))
	for idx := range indexSet {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })

	chart.Labels = make([]string, len(indices))
	for i, idx := range indices {
		chart.Labels[i] = formatBucketTime(idx)
	}
	for _, s := range seriesList {
		points := make([]int, len(indices))
		for i, idx := range indices {
			if d, ok := s.p99[idx]; ok {
				points[i] = int(d.Milliseconds())
			} else {
				points[i] = -1
			}
		}
		chart.Series = append(chart.Series, TimelineSeries{Name: s.name, P99: points})
	}
	return chart
}

// RenderHTML renders a report to w using the HTML template at templatePath.
// It only renders: no correlation, threshold, or output-file logic lives here —
// the caller decides where the rendered output goes.
func RenderHTML(data ReportData, templatePath string, w io.Writer) error {
	tmplData, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("reading template %q: %w", templatePath, err)
	}
	tmpl, err := template.New("report").Funcs(template.FuncMap{
		"formatDuration":    formatDuration,
		"formatBucketTime":  formatBucketTime,
		"formatStatusCodes": formatStatusCodes,
		"timelineColor":     timelineColor,
	}).Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parsing template %q: %w", templatePath, err)
	}
	return tmpl.Execute(w, data)
}

// formatDuration renders a time.Duration human-readably, e.g. "142ms".
func formatDuration(d time.Duration) string {
	return d.String()
}

// formatBucketTime renders a bucket index (a unix timestamp) as a clock time,
// e.g. "15:33:20".
func formatBucketTime(i int64) string {
	return time.Unix(i, 0).Format("15:04:05")
}

// formatStatusCodes renders an HTTP status-code histogram as "code×count"
// pairs sorted by code, e.g. "200×150, 404×3".
func formatStatusCodes(codes map[string]int) string {
	keys := make([]string, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, codes[k]))
	}
	return strings.Join(parts, ", ")
}

// timelineColor maps a runner name to its chart color. HTTP is ink, DB is
// muted gray, Redis gets red so it stands out from the first two.
func timelineColor(name string) string {
	switch name {
	case "HTTP":
		return "#fafafa"
	case "DB":
		return "#606060"
	case "Redis":
		return "#e0524d"
	}
	return "#767676"
}
