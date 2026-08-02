package barrage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExportJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	data := ReportData{
		Duration:    "10s",
		Ramp:        "3s",
		Concurrency: 20,
		CorrelationResult: CorrelationResult{
			Spikes: []CorrelatedSpike{
				{BucketIndex: 1700000000, Runner: "db", HTTPLatency: 250 * time.Millisecond, StorageLatency: 120 * time.Millisecond},
				{BucketIndex: 1700000001, Runner: "redis", HTTPLatency: 5 * time.Millisecond, StorageLatency: 2 * time.Second, Masked: true},
			},
		},
		Runners: []RunnerSummary{
			{Name: "HTTP", Requests: 100, Success: 99.5, P50: 2 * time.Millisecond, P99: 30 * time.Millisecond, StatusCodes: map[string]int{"200": 99, "500": 1}},
		},
		Timeline: TimelineChart{
			Labels: []string{"15:04:05"},
			Series: []TimelineSeries{{Name: "HTTP", P99: []int{30}}},
		},
	}

	if err := ExportJSON(data, path); err != nil {
		t.Fatalf("ExportJSON returned error: %v", err)
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var got JSONReport
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if got.Duration != "10s" || got.Ramp != "3s" || got.Concurrency != 20 {
		t.Errorf("metadata = %+v", got)
	}
	if got.GeneratedAt.IsZero() {
		t.Error("expected generated_at to be set")
	}
	if len(got.Runners) != 1 || got.Runners[0].Name != "HTTP" || got.Runners[0].P99MS != 30 {
		t.Errorf("runners = %+v", got.Runners)
	}
	if len(got.Spikes) != 2 || got.Spikes[0].Runner != "db" || got.Spikes[0].HTTPP99MS != 250 || got.Spikes[0].StorageP99MS != 120 {
		t.Errorf("spikes = %+v", got.Spikes)
	}
	if got.Spikes[1].Runner != "redis" || !got.Spikes[1].Masked || got.Spikes[1].StorageP99MS != 2000 {
		t.Errorf("redis masked spike = %+v", got.Spikes[1])
	}
	if len(got.Timeline.Series) != 1 || got.Timeline.Series[0].P99[0] != 30 {
		t.Errorf("timeline = %+v", got.Timeline)
	}
}

func TestExportJSONEmptyRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	if err := ExportJSON(ReportData{}, path); err != nil {
		t.Fatalf("ExportJSON returned error: %v", err)
	}

	var got JSONReport
	buf, _ := os.ReadFile(path)
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Runners != nil || got.Spikes == nil || got.Timeline.Series == nil {
		t.Errorf("expected empty slices to serialize as []: runners=%v spikes=%v series=%v",
			got.Runners, got.Spikes, got.Timeline.Series)
	}
}
