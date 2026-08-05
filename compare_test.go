package barrage

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompareRun(t *testing.T) {
	baseline := &JSONReport{
		Runners: []JSONRunner{
			{Name: "HTTP", P50MS: 2, P95MS: 5, P99MS: 30, MeanMS: 3},
			{Name: "DB", P50MS: 5, P95MS: 50, P99MS: 80, MeanMS: 12},
		},
	}
	current := &JSONReport{
		Runners: []JSONRunner{
			{Name: "HTTP", P50MS: 4, P95MS: 12, P99MS: 70, MeanMS: 6},
			{Name: "DB", P50MS: 6, P95MS: 60, P99MS: 100, MeanMS: 15},
		},
	}

	rows := CompareRun(baseline, current)

	if len(rows) != 2 {
		t.Fatalf("expected 2 comparison rows, got %d", len(rows))
	}

	var http Comparison
	for _, r := range rows {
		if r.Name == "HTTP" {
			http = r
		}
	}
	if http.Name != "HTTP" || http.BaselineP99 != 30 || http.CurrentP99 != 70 {
		t.Errorf("http row = %+v", http)
	}
	want := (70 - 30) * 100 / 30
	if http.PctChange != want {
		t.Errorf("http pct change = %d, want %d", http.PctChange, want)
	}
	if !http.Regressed(50) {
		t.Error("http should regress against a 50ms budget")
	}
	if http.Regressed(100) {
		t.Error("http should not regress against a 100ms budget")
	}
}

func TestCompareRunMissingRunner(t *testing.T) {
	baseline := &JSONReport{Runners: []JSONRunner{{Name: "HTTP", P99MS: 30}}}
	current := &JSONReport{Runners: []JSONRunner{{Name: "HTTP", P99MS: 40}}}

	rows := CompareRun(baseline, current)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	onlyCurrent := CompareRun(&JSONReport{}, current)
	if len(onlyCurrent) != 1 {
		t.Fatalf("expected 1 row for runner only in current, got %d", len(onlyCurrent))
	}
}

func TestCompareSpikes(t *testing.T) {
	baseline := &JSONReport{Spikes: []JSONSpike{
		{BucketTime: "12:00:01", Runner: "db", HTTPP99MS: 90, StorageP99MS: 150}, // worsened in current
		{BucketTime: "12:00:02", Runner: "redis", HTTPP99MS: 5, StorageP99MS: 300}, // fixed in current
		{BucketTime: "12:00:03", Runner: "db", HTTPP99MS: 200, StorageP99MS: 400}, // improved in current
	}}
	current := &JSONReport{Spikes: []JSONSpike{
		{BucketTime: "12:00:01", Runner: "db", HTTPP99MS: 95, StorageP99MS: 180}, // worsened
		{BucketTime: "12:00:03", Runner: "db", HTTPP99MS: 150, StorageP99MS: 250}, // improved
		{BucketTime: "12:00:04", Runner: "redis", HTTPP99MS: 10, StorageP99MS: 500}, // new
	}}

	rows := CompareSpikes(baseline, current)
	status := map[string]string{}
	for _, r := range rows {
		status[r.BucketTime+"/"+r.Runner] = r.Status
	}

	if got := status["12:00:01/db"]; got != "worsened" {
		t.Errorf("12:00:01/db = %q, want worsened", got)
	}
	if got := status["12:00:02/redis"]; got != "fixed" {
		t.Errorf("12:00:02/redis = %q, want fixed", got)
	}
	if got := status["12:00:03/db"]; got != "improved" {
		t.Errorf("12:00:03/db = %q, want improved", got)
	}
	if got := status["12:00:04/redis"]; got != "new" {
		t.Errorf("12:00:04/redis = %q, want new", got)
	}
	if len(rows) != 4 {
		t.Errorf("expected 4 spike rows, got %d", len(rows))
	}
}

func TestBuildCompareTimeline(t *testing.T) {
	baseline := &JSONReport{Timeline: JSONTimeline{
		Labels: []string{"12:00:00", "12:00:01"},
		Series: []JSONTimelineSeries{
			{Name: "HTTP", P99: []int{10, 90}},
		},
	}}
	current := &JSONReport{Timeline: JSONTimeline{
		Labels: []string{"12:00:01", "12:00:02"},
		Series: []JSONTimelineSeries{
			{Name: "HTTP", P99: []int{95, 200}},
		},
	}}

	chart := BuildCompareTimeline(baseline, current)
	if len(chart.Labels) != 3 {
		t.Fatalf("expected 3 union labels, got %d: %v", len(chart.Labels), chart.Labels)
	}
	if len(chart.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(chart.Series))
	}
	s := chart.Series[0]
	wantBase := []int{10, 90, -1}
	for i, v := range wantBase {
		if s.Baseline[i] != v {
			t.Errorf("baseline[%d] = %d, want %d", i, s.Baseline[i], v)
		}
	}
	wantCur := []int{-1, 95, 200}
	for i, v := range wantCur {
		if s.Current[i] != v {
			t.Errorf("current[%d] = %d, want %d", i, s.Current[i], v)
		}
	}
}

func TestRenderCompare(t *testing.T) {
	base := &JSONReport{Runners: []JSONRunner{{Name: "HTTP", P99MS: 30}}}
	cur := &JSONReport{Runners: []JSONRunner{{Name: "HTTP", P99MS: 70}}}
	data := NewCompareReportData(base, cur, 50)

	var buf bytes.Buffer
	if err := RenderCompare(data, "templates/compare.html", &buf); err != nil {
		t.Fatalf("RenderCompare returned error: %v", err)
	}
	if !strings.Contains(buf.String(), "regression") {
		t.Error("expected regression status in rendered report")
	}
	if !strings.Contains(buf.String(), "HTTP") {
		t.Error("expected runner name in rendered report")
	}
}