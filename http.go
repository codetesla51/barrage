package barrage

import (
	"net/http"
	"sort"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

type HTTPTarget struct {
	Method string      `yaml:"method"`
	URL    string      `yaml:"url"`
	Body   []byte      `yaml:"body"`
	Header http.Header `yaml:"header"`
}

type HTTPResult struct {
	// Aggregate summary — for the final report
	Requests    uint64
	Success     float64
	StatusCodes map[string]int
	Errors      []string

	P50  time.Duration
	P95  time.Duration
	P99  time.Duration
	Max  time.Duration
	Mean time.Duration

	Rate       float64
	Throughput float64

	Earliest time.Time
	Latest   time.Time

	Buckets []HTTPBucket
}

type HTTPBucket struct {
	Start       time.Time
	End         time.Time
	Requests    uint64
	Success     float64
	P50         time.Duration
	P99         time.Duration
	StatusCodes map[string]int
}

func FireHTTP(target HTTPTarget, rate int, duration time.Duration, bucketWidth time.Duration) (*HTTPResult, error) {
	targeter := vegeta.NewStaticTargeter(vegeta.Target{
		Method: target.Method,
		URL:    target.URL,
		Body:   target.Body,
		Header: target.Header,
	})
	attacker := vegeta.NewAttacker()
	pace := vegeta.Rate{Freq: rate, Per: time.Second}

	var overall vegeta.Metrics
	bucketed := make(map[int64][]*vegeta.Result) // bucket index -> results

	for sample := range attacker.Attack(targeter, pace, duration, "load-test") {
		overall.Add(sample)

		idx := sample.Timestamp.Unix() / int64(bucketWidth.Seconds())
		bucketed[idx] = append(bucketed[idx], sample)
	}
	overall.Close()

	result := &HTTPResult{
		Requests:    overall.Requests,
		Success:     overall.Success,
		StatusCodes: overall.StatusCodes,
		Errors:      overall.Errors,
		P50:         overall.Latencies.P50,
		P95:         overall.Latencies.P95,
		P99:         overall.Latencies.P99,
		Max:         overall.Latencies.Max,
		Mean:        overall.Latencies.Mean,
		Rate:        overall.Rate,
		Throughput:  overall.Throughput,
		Earliest:    overall.Earliest,
		Latest:      overall.Latest,
	}

	result.Buckets = buildHTTPBuckets(bucketed, bucketWidth)

	return result, nil
}

func buildHTTPBuckets(bucketed map[int64][]*vegeta.Result, width time.Duration) []HTTPBucket {
	indices := make([]int64, 0, len(bucketed))
	for idx := range bucketed {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })

	buckets := make([]HTTPBucket, 0, len(indices))

	for _, idx := range indices {
		results := bucketed[idx]

		var metrics vegeta.Metrics
		for _, sample := range results {
			metrics.Add(sample)
		}
		metrics.Close()

		start := time.Unix(idx*int64(width.Seconds()), 0)
		buckets = append(buckets, HTTPBucket{
			Start:       start,
			End:         start.Add(width),
			Requests:    metrics.Requests,
			Success:     metrics.Success,
			P50:         metrics.Latencies.P50,
			P99:         metrics.Latencies.P99,
			StatusCodes: metrics.StatusCodes,
		})
	}

	return buckets
}
