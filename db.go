package barrage

import (
	"database/sql"
	"fmt"
	"math/rand"
	"sort"
	"time"
)

// DBTarget represents a database target for load testing.
type DBTarget struct {
	Conn      string
	Driver    string
	Query     []QueryWeight
	Args      []any
	QueryType string
}
type QueryWeight struct {
	Query  string
	Weight int
}

// Whole Runs Summery of results, aggregated into a single result.
type DBResult struct {
	Requests   uint64
	Success    float64
	Errors     []string
	P50        time.Duration
	P95        time.Duration
	P99        time.Duration
	Max        time.Duration
	Mean       time.Duration
	Rate       float64
	Throughput float64
	Earliest   time.Time
	Latest     time.Time
	Buckets    []Bucket
}

// One time window of results, aggregated into a single bucket.
type Bucket struct {
	Start    int64
	End      int64
	Requests uint64
	Success  time.Duration
	P50      time.Duration
	P99      time.Duration
}

// dbQueryResult represents the result of a single database query execution.
type dbQueryResult struct {
	Timestamp time.Time
	Latency   time.Duration
	Success   bool
	Err       error
}

// OpenConnection opens a database connection using the provided connection string.
func OpenConnection(conn string, driver string) (*sql.DB, error) {
	if driver == "" {
		return nil, fmt.Errorf("driver is required")
	}
	db, err := sql.Open(driver, conn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// FireDB executes database queries according to the specified target and parameters.
func FireDB(target DBTarget, rate int, duration time.Duration, bucketWidth time.Duration) (*DBResult, error) {
	db, err := OpenConnection(target.Conn, target.Driver)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	// calculate the interval between queries based on the specified rate
	// time.duration (time.Second) is divided by the rate to get the interval between queries
	// example: if rate is 10 queries per second, interval will be 100ms
	interval := time.Duration(int(time.Second) / rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timeout := time.After(duration)

	overall := []dbQueryResult{}
	runStart := time.Now()
	// Loop through the ticker and execute queries at the specified rate until the timeout is reached.
Loop:
	for {
		select {
		case <-ticker.C:
			startTime := time.Now()
			var err error
			// use round-robin selection to pick a query from the target's query list
			// for each tick, the index is calculated using the counter modulo the length of the query list
			// example: if there are 3 queries and the counter is 5, the index will be 2 (5 % 3 = 2)

			pickedQuery := pickQuery(cumulativeWeights(target.Query))
			if target.QueryType == "read" {
				var rows *sql.Rows
				rows, err = db.Query(pickedQuery, target.Args...)
				if err == nil {
					rows.Close()
				}
			} else {
				_, err = db.Exec(pickedQuery, target.Args...)
			}
			elapsed := time.Since(startTime)
			overall = append(overall, dbQueryResult{
				Timestamp: startTime,
				Latency:   elapsed,
				Success:   err == nil,
				Err:       err,
			})
		case <-timeout:
			break Loop
		}
	}

	return buildDBResult(overall, runStart, bucketWidth, duration), nil
}

// split results into buckets based on the specified bucket width and calculate statistics for each bucket.
func buildDBResult(overall []dbQueryResult, runStart time.Time, bucketWidth, duration time.Duration) *DBResult {
	res := &DBResult{
		Requests: uint64(len(overall)),
		Earliest: runStart,
		Latest:   time.Now(),
		Buckets:  buildDBBuckets(overall, runStart, bucketWidth),
	}
	if len(overall) == 0 {
		return res
	}

	latencies := make([]time.Duration, 0, len(overall))
	var successN uint64
	var sum time.Duration
	errSeen := map[string]bool{}

	for _, sample := range overall {
		latencies = append(latencies, sample.Latency)
		sum += sample.Latency
		if sample.Success {
			successN++
		} else if sample.Err != nil && !errSeen[sample.Err.Error()] {
			errSeen[sample.Err.Error()] = true
			res.Errors = append(res.Errors, sample.Err.Error())
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	res.Success = float64(successN) / float64(len(overall))
	res.Mean = sum / time.Duration(len(overall))
	res.Max = latencies[len(latencies)-1]
	res.P50 = percentile(latencies, 0.50)
	res.P95 = percentile(latencies, 0.95)
	res.P99 = percentile(latencies, 0.99)

	secs := duration.Seconds()
	if secs > 0 {
		res.Rate = float64(len(overall)) / secs
		res.Throughput = float64(successN) / secs
	}

	return res
}

func buildDBBuckets(results []dbQueryResult, runStart time.Time, bucketWidth time.Duration) []Bucket {
	if len(results) == 0 || bucketWidth <= 0 {
		return nil
	}

	type bucketAgg struct {
		bucket    Bucket
		latencies []time.Duration
		successN  uint64
	}

	aggs := make(map[int64]*bucketAgg)

	for _, sample := range results {
		idx := int64(sample.Timestamp.Sub(runStart) / bucketWidth)
		a, ok := aggs[idx]
		if !ok {
			start := runStart.Add(time.Duration(idx) * bucketWidth)
			a = &bucketAgg{
				bucket: Bucket{
					Start: start.Unix(),
					End:   start.Add(bucketWidth).Unix(),
				},
			}
			aggs[idx] = a
		}
		a.bucket.Requests++
		if sample.Success {
			a.successN++
		}
		a.latencies = append(a.latencies, sample.Latency)
	}

	indices := make([]int64, 0, len(aggs))
	for idx := range aggs {
		indices = append(indices, idx)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })

	buckets := make([]Bucket, 0, len(indices))
	for _, idx := range indices {
		a := aggs[idx]
		sort.Slice(a.latencies, func(i, j int) bool { return a.latencies[i] < a.latencies[j] })
		a.bucket.P50 = percentile(a.latencies, 0.50)
		a.bucket.P99 = percentile(a.latencies, 0.99)
		a.bucket.Success = time.Duration(a.successN)
		buckets = append(buckets, a.bucket)
	}
	return buckets
}

// percentile calculates the p-th percentile of a sorted slice of time.Duration values.
// If the slice is empty, it returns 0. The index is calculated as int(p * len(sorted)), and if it exceeds the length of the slice, it is capped to the last index.
// example: for a sorted slice of 5 elements and p=0.50, the index will be int(0.5*5) = 2, returning the value at index 2.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
func cumulativeWeights(queries []QueryWeight) []QueryWeight {
	currentWeight := 0
	weighted := []QueryWeight{}
	for _, q := range queries {
		currentWeight += q.Weight
		weighted = append(weighted, QueryWeight{
			Query:  q.Query,
			Weight: currentWeight,
		})
	}
	return weighted

}
func pickQuery(queries []QueryWeight) string {
	if len(queries) == 0 {
		return ""
	}
	total := queries[len(queries)-1].Weight
	picked := ""

	randWeight := randInt(0, total)
	for _, q := range queries {
		if randWeight < q.Weight {
			picked = q.Query
			break
		}
	}
	return picked
}
func randInt(min, max int) int {
	return rand.Intn(max-min) + min
}
