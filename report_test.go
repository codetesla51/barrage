package barrage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testTemplatePath = filepath.Join("templates", "report.html")

func testReportData() ReportData {
	return ReportData{
		CorrelationResult: CorrelationResult{
			Spikes: []CorrelatedSpike{
				{BucketIndex: 1700000000, HTTPLatency: 142 * time.Millisecond, DBLatency: 987 * time.Millisecond},
				{BucketIndex: 1700000001, HTTPLatency: 250 * time.Millisecond, DBLatency: time.Second},
			},
		},
		Runners: []RunnerSummary{
			{
				Name:        "HTTP",
				Requests:    100,
				Success:     0,
				P50:         3 * time.Millisecond,
				P99:         9 * time.Millisecond,
				Max:         10 * time.Millisecond,
				Mean:        4 * time.Millisecond,
				Rate:        10,
				Throughput:  0,
				StatusCodes: map[string]int{"0": 100},
			},
		},
	}
}

func TestRenderHTML(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderHTML(testReportData(), testTemplatePath, &buf); err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}

	out := buf.String()
	first := time.Unix(1700000000, 0).Format("15:04:05")
	second := time.Unix(1700000001, 0).Format("15:04:05")
	for _, want := range []string{
		"2 flagged",
		first,
		"142ms",
		"987ms",
		second,
		"250ms",
		"1s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if strings.Contains(out, "1700000000") {
		t.Error("expected bucket labels to be human-readable times, not unix timestamps")
	}
	if strings.Contains(out, "No correlated spikes") {
		t.Error("expected spike table, got empty-state message")
	}
}

func TestRenderHTMLRunSummary(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderHTML(testReportData(), testTemplatePath, &buf); err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"run summary",
		">HTTP<",
		"100",
		"0.0%",
		"3ms",
		"9ms",
		"0×100",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("run summary missing %q", want)
		}
	}
}

func TestRenderHTMLNoSpikes(t *testing.T) {
	data := ReportData{
		Runners: []RunnerSummary{
			{Name: "DB", Requests: 50, Success: 100, P50: time.Millisecond, P99: 5 * time.Millisecond},
		},
	}
	var buf bytes.Buffer
	if err := RenderHTML(data, testTemplatePath, &buf); err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "No correlated spikes") {
		t.Errorf("expected empty-state message, got: %s", out)
	}
	if strings.Contains(out, "2 flagged") {
		t.Error("expected no spike summary for empty result")
	}
	if !strings.Contains(out, "100.0%") {
		t.Error("expected DB runner summary with 100% success")
	}
}

func TestRenderHTMLTimeline(t *testing.T) {
	data := ReportData{
		Timeline: TimelineChart{
			Labels: []string{"15:04:05", "15:04:06"},
			Series: []TimelineSeries{
				{Name: "HTTP", P99: []int{5, 250}},
				{Name: "Redis", P99: []int{3, -1}},
			},
		},
	}
	var buf bytes.Buffer
	if err := RenderHTML(data, testTemplatePath, &buf); err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"latency timeline",
		"15:04:05",
		"15:04:06",
		"5 , 250",
		"null,",
		"Redis",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("timeline missing %q", want)
		}
	}
}

func TestRenderHTMLNoTimeline(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderHTML(ReportData{}, testTemplatePath, &buf); err != nil {
		t.Fatalf("RenderHTML returned error: %v", err)
	}
	if strings.Contains(buf.String(), "latency timeline") {
		t.Error("expected no timeline section when no runner data exists")
	}
}

func TestBuildTimeline(t *testing.T) {
	base := time.Unix(1700000000, 0)
	result := &OrchestratorResult{
		HTTPResult: &HTTPResult{Buckets: []HTTPBucket{
			{Start: base, P99: 5 * time.Millisecond},
			{Start: base.Add(time.Second), P99: 250 * time.Millisecond},
		}},
		DBResult: &DBResult{Buckets: []Bucket{
			{Start: 1700000000, P99: 4 * time.Millisecond},
			{Start: 1700000002, P99: 300 * time.Millisecond},
		}},
	}

	tl := buildTimeline(result)
	if len(tl.Labels) != 3 {
		t.Fatalf("expected 3 timeline labels, got %d: %v", len(tl.Labels), tl.Labels)
	}
	wantLabel := func(sec int64) string { return time.Unix(sec, 0).Format("15:04:05") }
	if tl.Labels[0] != wantLabel(1700000000) || tl.Labels[1] != wantLabel(1700000001) || tl.Labels[2] != wantLabel(1700000002) {
		t.Errorf("labels = %v", tl.Labels)
	}

	if len(tl.Series) != 2 {
		t.Fatalf("expected 2 series, got %d", len(tl.Series))
	}
	http := tl.Series[0]
	if http.Name != "HTTP" || len(http.P99) != 3 {
		t.Fatalf("http series = %+v", http)
	}
	if http.P99[0] != 5 || http.P99[1] != 250 || http.P99[2] != -1 {
		t.Errorf("http p99 = %v, want [5 250 -1]", http.P99)
	}
	db := tl.Series[1]
	if db.Name != "DB" || db.P99[0] != 4 || db.P99[1] != -1 || db.P99[2] != 300 {
		t.Errorf("db p99 = %v, want [4 -1 300]", db.P99)
	}
}

func TestRenderHTMLMissingTemplate(t *testing.T) {
	err := RenderHTML(ReportData{}, filepath.Join(t.TempDir(), "missing.html"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for missing template")
	}
	if !strings.Contains(err.Error(), "reading template") {
		t.Errorf("expected clear error about reading template, got: %v", err)
	}
}

func TestRenderHTMLBadTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.html")
	if err := os.WriteFile(path, []byte("{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RenderHTML(ReportData{}, path, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for unparseable template")
	}
	if !strings.Contains(err.Error(), "parsing template") {
		t.Errorf("expected clear error about parsing template, got: %v", err)
	}
}

func TestFormatStatusCodes(t *testing.T) {
	got := formatStatusCodes(map[string]int{"200": 150, "0": 100})
	if got != "0×100, 200×150" {
		t.Errorf("formatStatusCodes = %q, want %q", got, "0×100, 200×150")
	}
}

func TestNewReportDataRunners(t *testing.T) {
	data := NewReportData(&OrchestratorResult{
		HTTPResult: &HTTPResult{Requests: 1, StatusCodes: map[string]int{"200": 1}},
		DBResult:   &DBResult{Requests: 2},
		RedisResult: &RedisResult{
			Requests: 3,
		},
	}, CorrelationResult{})

	if len(data.Runners) != 3 {
		t.Fatalf("expected 3 runner summaries, got %d", len(data.Runners))
	}
	if data.Runners[0].Name != "HTTP" || data.Runners[1].Name != "DB" || data.Runners[2].Name != "Redis" {
		t.Errorf("runner order/names = %+v", data.Runners)
	}
	if len(data.Runners[0].StatusCodes) != 1 {
		t.Errorf("expected HTTP status codes to be carried through, got %v", data.Runners[0].StatusCodes)
	}
}
