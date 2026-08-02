package barrage

import (
	"database/sql"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// DBTarget represents a database target for load testing.
type DBTarget struct {
	Conn   string        `yaml:"conn"`
	Driver string        `yaml:"driver"`
	Query  []QueryWeight `yaml:"queries"`
}

// QueryWeight is one weighted entry in a runner's query list. Type and Args
// are authoritative per query: Type routes read vs write, Args are bound to
// that query only. An empty Type falls back to auto-detection.
type QueryWeight struct {
	Query  string `yaml:"query"`
	Weight int    `yaml:"weight"`
	Type   string `yaml:"type"`
	Args   []any  `yaml:"args"`
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

// FireDB executes database queries according to the specified target and
// parameters. Queries are fired at rate per second (ramping up over ramp if
// set) and run concurrently on a worker pool with up to concurrency workers.
func FireDB(target DBTarget, rate, concurrency int, duration, bucketWidth, ramp time.Duration) (*DBResult, error) {
	db, err := OpenConnection(target.Conn, target.Driver)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	overall, start := runPaced(rate, concurrency, duration, ramp, func() dbQueryResult {
		pick := pickQuery(cumulativeWeights(target.Query))
		queryStart := time.Now()
		var err error
		if queryIsRead(pick) {
			var rows *sql.Rows
			rows, err = db.Query(pick.Query, pick.Args...)
			if err == nil {
				rows.Close()
			}
		} else {
			_, err = db.Exec(pick.Query, pick.Args...)
		}
		return dbQueryResult{Latency: time.Since(queryStart), Success: err == nil, Err: err}
	})

	return buildDBResult(overall, start, bucketWidth, duration), nil
}

// split results into buckets based on the specified bucket width and calculate statistics for each bucket.
func buildDBResult(overall []dbQueryResult, runStart time.Time, bucketWidth, duration time.Duration) *DBResult {
	res := &DBResult{
		Requests: uint64(len(overall)),
		Earliest: runStart,
		Latest:   runStart.Add(duration),
		Buckets:  buildDBBuckets(overall, bucketWidth),
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

// buildDBBuckets groups results into buckets aligned to wall-clock seconds
// (same base as the HTTP runner), so bucket indices match across runners
// regardless of when each one started. Buckets with no results are omitted.
func buildDBBuckets(results []dbQueryResult, bucketWidth time.Duration) []Bucket {
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
		idx := sample.Timestamp.Unix() / int64(bucketWidth.Seconds())
		a, ok := aggs[idx]
		if !ok {
			start := time.Unix(idx*int64(bucketWidth.Seconds()), 0)
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
func pickQuery(queries []QueryWeight) QueryWeight {
	if len(queries) == 0 {
		return QueryWeight{}
	}
	total := queries[len(queries)-1].Weight
	randWeight := randInt(0, total)
	for _, q := range queries {
		if randWeight < q.Weight {
			return q
		}
	}
	return queries[len(queries)-1]
}
func randInt(min, max int) int {
	return rand.Intn(max-min) + min
}

// queryIsRead reports whether a picked query should run through Query rather
// than Exec. The per-query type is authoritative; when it is unset, routing
// falls back to a heuristic on the SQL text.
func queryIsRead(q QueryWeight) bool {
	switch strings.ToLower(strings.TrimSpace(q.Type)) {
	case "read":
		return true
	case "write":
		return false
	}
	return isReadQuery(q.Query)
}

// isReadQuery is the fallback heuristic for routing when a query has no
// explicit type: anything that mutates rows (a RETURNING clause) or is not a
// read statement (SELECT/SHOW/EXPLAIN/WITH) goes through Exec. Prefer setting
// an explicit type per query; this only guesses.
func isReadQuery(q string) bool {
	u := strings.ToUpper(strings.TrimSpace(q))
	if strings.Contains(u, "RETURNING") {
		return false
	}
	return strings.HasPrefix(u, "SELECT") ||
		strings.HasPrefix(u, "SHOW") ||
		strings.HasPrefix(u, "EXPLAIN") ||
		strings.HasPrefix(u, "WITH")
}
