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

func comparePreviewData() CompareReportData {
	baseTime := time.Unix(1700000000, 0)
	labels := make([]string, 12)
	for i := range labels {
		labels[i] = baseTime.Add(time.Duration(i) * time.Second).Format("15:04:05")
	}

	baseline := &JSONReport{
		GeneratedAt: baseTime,
		Duration:    "30s",
		Ramp:        "5s",
		Concurrency: 32,
		Runners: []JSONRunner{
			{Name: "HTTP", P99MS: 246, P50MS: 9, P95MS: 84},
			{Name: "DB", P99MS: 210, P50MS: 5, P95MS: 62},
			{Name: "Redis", P99MS: 63, P50MS: 1, P95MS: 9},
		},
		Spikes: []JSONSpike{
			{BucketTime: labels[2], Runner: "db", HTTPP99MS: 120, StorageP99MS: 820},
			{BucketTime: labels[3], Runner: "redis", HTTPP99MS: 180, StorageP99MS: 940},
			{BucketTime: labels[5], Runner: "db", HTTPP99MS: 200, StorageP99MS: 1100},
			{BucketTime: labels[7], Runner: "db", HTTPP99MS: 260, StorageP99MS: 1320},
		},
		Timeline: JSONTimeline{
			Labels: labels,
			Series: []JSONTimelineSeries{
				{Name: "HTTP", P99: []int{11, 14, 18, 22, 26, 31, 38, 44, 52, 61, 70, 84}},
				{Name: "DB", P99: []int{4, 5, 5, 6, 7, 9, 11, 13, 16, 19, 24, 30}},
				{Name: "Redis", P99: []int{2, 2, 3, 3, 3, 4, 4, 5, 6, 8, 9, 12}},
			},
		},
	}

	current := &JSONReport{
		GeneratedAt: baseTime.Add(3600 * time.Second),
		Duration:    "30s",
		Ramp:        "5s",
		Concurrency: 32,
		Runners: []JSONRunner{
			{Name: "HTTP", P99MS: 380, P50MS: 14, P95MS: 120},
			{Name: "DB", P99MS: 195, P50MS: 4, P95MS: 58},
			{Name: "Redis", P99MS: 71, P50MS: 2, P95MS: 12},
		},
		Spikes: []JSONSpike{
			{BucketTime: labels[2], Runner: "db", HTTPP99MS: 140, StorageP99MS: 980},
			{BucketTime: labels[3], Runner: "redis", HTTPP99MS: 210, StorageP99MS: 1120},
			{BucketTime: labels[5], Runner: "db", HTTPP99MS: 310, StorageP99MS: 1680},
			{BucketTime: labels[6], Runner: "redis", HTTPP99MS: 190, StorageP99MS: 860},
			{BucketTime: labels[8], Runner: "db", HTTPP99MS: 280, StorageP99MS: 1420},
		},
		Timeline: JSONTimeline{
			Labels: labels,
			Series: []JSONTimelineSeries{
				{Name: "HTTP", P99: []int{13, 16, 21, 26, 32, 40, 52, 68, 88, 120, 165, 210}},
				{Name: "DB", P99: []int{4, 5, 6, 7, 8, 10, 13, 16, 20, 26, 34, 48}},
				{Name: "Redis", P99: []int{2, 3, 3, 4, 4, 5, 6, 7, 8, 10, 13, 18}},
			},
		},
	}

	data := NewCompareReportData(baseline, current, 250)
	data.BaselineName = "baseline-2024-11-14T23:13:00Z.json"
	data.CurrentName = "current-2024-11-14T23:18:00Z.json"
	return data
}

func compareStressPreviewData(data CompareReportData) CompareReportData {
	baseTime := time.Unix(1700000000, 0)
	// many spikes to exercise the capped table
	spikes := make([]SpikeComparison, 0, 120)
	for i := 0; i < 120; i++ {
		status := "unchanged"
		switch i % 5 {
		case 0:
			status = "new"
		case 1:
			status = "worsened"
		case 2:
			status = "fixed"
		case 3:
			status = "improved"
		}
		spikes = append(spikes, SpikeComparison{
			BucketTime:      baseTime.Add(time.Duration(i) * time.Second).Format("15:04:05"),
			Runner:          []string{"db", "redis", "postgres-primary-replica-2-eu-west-1-readonly"}[i%3],
			BaselineHTTPMS:  int64(80 + i%40),
			BaselineStoreMS: int64(600 + (i*17)%400),
			CurrentHTTPMS:   int64(90 + i%50),
			CurrentStoreMS:  int64(620 + (i*23)%520),
			Status:          status,
		})
	}
	data.Spikes = spikes
	return data
}

// TestComparePreview renders sample compare reports to /tmp so the template can
// be eyeballed in a browser after a CSS or layout change. Run it with:
//
//	BARRAGE_PREVIEW_OUT=/tmp/barrage-preview/compare.html go test -run TestComparePreview -v .
func TestComparePreview(t *testing.T) {
	out := os.Getenv("BARRAGE_PREVIEW_OUT")
	if out == "" {
		out = "/tmp/barrage-preview/compare.html"
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"full", "stress"} {
		data := comparePreviewData()
		if variant == "stress" {
			data = compareStressPreviewData(data)
		}
		var buf bytes.Buffer
		if err := RenderCompare(data, "templates/compare.html", &buf); err != nil {
			t.Fatalf("render %s: %v", variant, err)
		}
		path := strings.TrimSuffix(out, ".html") + "-" + variant + ".html"
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PREVIEW compare %s: %s (%d bytes)\n", variant, path, buf.Len())
	}
	// also emit the timeline script snippet for eyeballing
	var buf bytes.Buffer
	if err := RenderCompare(comparePreviewData(), "templates/compare.html", &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	if i := strings.Index(html, "tDatasets.push"); i >= 0 {
		end := i + 240
		if end > len(html) {
			end = len(html)
		}
		fmt.Printf("---- compare timeline script ----\n%s\n-------------------------------\n", html[i:end])
	}
}
