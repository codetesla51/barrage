package barrage

import (
	"fmt"
	"html/template"
	"io"
	"os"
	"sort"
	"strconv"
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

// ReportData is the full data model for the HTML report: the correlated-spike
// analysis plus one RunnerSummary per runner that ran.
type ReportData struct {
	CorrelationResult
	Runners []RunnerSummary
}

// NewReportData assembles a ReportData from an OrchestratorResult and the
// correlation analysis. Runners that did not run are omitted.
func NewReportData(result *OrchestratorResult, correlation CorrelationResult) ReportData {
	data := ReportData{CorrelationResult: correlation}
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

// formatBucketTime renders a bucket index (a unix timestamp) as a label.
func formatBucketTime(i int64) string {
	return strconv.FormatInt(i, 10)
}

// formatStatusCodes renders an HTTP status-code histogram as "code:count"
// pairs sorted by code, e.g. "0:100, 200:150".
func formatStatusCodes(codes map[string]int) string {
	keys := make([]string, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, codes[k]))
	}
	return strings.Join(parts, ", ")
}
