package barrage

import "time"

// CorrelatedSpike records a bucket where both the HTTP and DB latencies
// crossed their respective thresholds at the same bucket index.
type CorrelatedSpike struct {
	BucketIndex int64
	HTTPLatency time.Duration
	DBLatency   time.Duration
}

// CorrelationResult holds all correlated spikes found between the HTTP and
// DB runs.
type CorrelationResult struct {
	Spikes []CorrelatedSpike
}

// Correlate finds buckets where both the HTTP and DB latencies exceeded their
// respective thresholds at the same bucket index.
//
// Buckets are matched by index derived from each bucket's Start time
// (HTTPBucket.Start.Unix() and Bucket.Start), so both runners must use the
// same bucket width for indices to align. A bucket present on only one side is
// skipped. Thresholds are fixed absolute cutoffs, compared against the P99
// latency of each bucket.
func Correlate(result *OrchestratorResult, httpThreshold, dbThreshold time.Duration) CorrelationResult {
	if result == nil || result.HTTPResult == nil || result.DBResult == nil {
		return CorrelationResult{}
	}

	dbByIndex := make(map[int64]Bucket, len(result.DBResult.Buckets))
	for _, dbBucket := range result.DBResult.Buckets {
		dbByIndex[dbBucket.Start] = dbBucket
	}

	var spikes []CorrelatedSpike
	for _, httpBucket := range result.HTTPResult.Buckets {
		index := httpBucket.Start.Unix()
		dbBucket, ok := dbByIndex[index]
		if !ok {
			continue
		}
		if httpBucket.P99 > httpThreshold && dbBucket.P99 > dbThreshold {
			spikes = append(spikes, CorrelatedSpike{
				BucketIndex: index,
				HTTPLatency: httpBucket.P99,
				DBLatency:   dbBucket.P99,
			})
		}
	}
	return CorrelationResult{Spikes: spikes}
}
