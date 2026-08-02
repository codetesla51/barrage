package barrage

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// JSONReport is the machine-readable export of a run, written by ExportJSON.
// Latencies are in milliseconds so consumers can compare runs without parsing
// strings.
type JSONReport struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Duration    string       `json:"duration"`
	Ramp        string       `json:"ramp"`
	Concurrency int          `json:"concurrency"`
	Runners     []JSONRunner `json:"runners"`
	Spikes      []JSONSpike  `json:"spikes"`
	Timeline    JSONTimeline `json:"timeline"`
}

// JSONRunner is one runner's aggregate summary in the JSON export.
type JSONRunner struct {
	Name        string         `json:"name"`
	Requests    uint64         `json:"requests"`
	Success     float64        `json:"success_percent"`
	P50MS       int64          `json:"p50_ms"`
	P95MS       int64          `json:"p95_ms"`
	P99MS       int64          `json:"p99_ms"`
	MaxMS       int64          `json:"max_ms"`
	MeanMS      int64          `json:"mean_ms"`
	Rate        float64        `json:"rate"`
	Throughput  float64        `json:"throughput"`
	StatusCodes map[string]int `json:"status_codes,omitempty"`
}

// JSONSpike is one storage spike (DB or Redis) in the JSON export. Runner names
// the storage runner that triggered the spike; Masked marks a bucket where HTTP
// did not cross its threshold.
type JSONSpike struct {
	BucketTime   string `json:"bucket_time"`
	Runner       string `json:"runner"`
	HTTPP99MS    int64  `json:"http_p99_ms"`
	StorageP99MS int64  `json:"storage_p99_ms"`
	Masked       bool   `json:"masked,omitempty"`
}

// JSONTimeline is one runner's per-bucket P99 latency in the JSON export.
type JSONTimeline struct {
	Labels []string             `json:"labels"`
	Series []JSONTimelineSeries `json:"series"`
}

// JSONTimelineSeries is a single runner's P99 line in the JSON export.
type JSONTimelineSeries struct {
	Name string `json:"name"`
	P99  []int  `json:"p99_ms"` // -1 = no request in that bucket
}

// ExportJSON writes a machine-readable JSON summary of a run to path.
func ExportJSON(data ReportData, path string) error {
	buf, err := BuildJSON(data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return fmt.Errorf("writing JSON report %q: %w", path, err)
	}
	return nil
}

// BuildJSON renders a ReportData as the machine-readable JSON report. It is
// shared by ExportJSON and the report template's in-page export button so both
// produce identical output.
func BuildJSON(data ReportData) ([]byte, error) {
	report := JSONReport{
		GeneratedAt: time.Now(),
		Duration:    data.Duration,
		Ramp:        data.Ramp,
		Concurrency: data.Concurrency,
		Spikes:      make([]JSONSpike, 0, len(data.Spikes)),
		Timeline: JSONTimeline{
			Labels: data.Timeline.Labels,
			Series: make([]JSONTimelineSeries, 0, len(data.Timeline.Series)),
		},
	}
	for _, s := range data.Spikes {
		report.Spikes = append(report.Spikes, JSONSpike{
			BucketTime:   formatBucketTime(s.BucketIndex),
			Runner:       s.Runner,
			HTTPP99MS:    s.HTTPLatency.Milliseconds(),
			StorageP99MS: s.StorageLatency.Milliseconds(),
			Masked:       s.Masked,
		})
	}
	for _, s := range data.Timeline.Series {
		report.Timeline.Series = append(report.Timeline.Series, JSONTimelineSeries{Name: s.Name, P99: s.P99})
	}
	for _, r := range data.Runners {
		report.Runners = append(report.Runners, JSONRunner{
			Name:        r.Name,
			Requests:    r.Requests,
			Success:     r.Success,
			P50MS:       r.P50.Milliseconds(),
			P95MS:       r.P95.Milliseconds(),
			P99MS:       r.P99.Milliseconds(),
			MaxMS:       r.Max.Milliseconds(),
			MeanMS:      r.Mean.Milliseconds(),
			Rate:        r.Rate,
			Throughput:  r.Throughput,
			StatusCodes: r.StatusCodes,
		})
	}

	buf, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding JSON: %w", err)
	}
	return append(buf, '\n'), nil
}
