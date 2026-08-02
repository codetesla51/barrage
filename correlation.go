package barrage

import (
	"sort"
	"time"
)

// CorrelatedSpike records a bucket where a storage runner's (DB or Redis) P99
// crossed its threshold. Runner names the storage runner that triggered the
// spike; HTTP latency is recorded alongside it as the app-side reference.
// Masked marks buckets where HTTP stayed under its threshold while the storage
// runner spiked, so the two categories stay distinguishable in the report.
type CorrelatedSpike struct {
	BucketIndex    int64
	Runner         string // "db" or "redis"
	HTTPLatency    time.Duration
	StorageLatency time.Duration
	Masked         bool
}

// CorrelationResult holds all spikes found between the HTTP run and the
// storage runners (DB and Redis).
type CorrelationResult struct {
	Spikes []CorrelatedSpike
}

// Correlate finds buckets where the DB or Redis latency exceeded its threshold.
//
// Buckets are matched by index derived from each bucket's Start time
// (HTTPBucket.Start.Unix() and Bucket.Start), so all runners must use the same
// bucket width for indices to align. A bucket present on only one side is
// skipped. Thresholds are fixed absolute cutoffs, compared against the P99
// latency of each bucket. Spikes are sorted chronologically.
//
// Two categories are reported:
//   - dual spike: the storage runner's P99 and HTTP P99 both crossed their
//     thresholds — the correlated latency jump the report is built around;
//   - masked: the storage runner's P99 crossed its threshold while HTTP stayed
//     under its own. HTTP latency is recorded for context even though it did
//     not spike. HTTP-only buckets are deliberately not flagged: a slow HTTP
//     endpoint that leaves the datastores idle is an app problem, not a
//     storage one.
func Correlate(result *OrchestratorResult, httpThreshold, dbThreshold, redisThreshold time.Duration) CorrelationResult {
	if result == nil || result.HTTPResult == nil {
		return CorrelationResult{}
	}

	httpByIndex := make(map[int64]HTTPBucket, len(result.HTTPResult.Buckets))
	for _, b := range result.HTTPResult.Buckets {
		httpByIndex[b.Start.Unix()] = b
	}

	storageByIndex := func(buckets []Bucket) map[int64]Bucket {
		m := make(map[int64]Bucket, len(buckets))
		for _, b := range buckets {
			m[b.Start] = b
		}
		return m
	}

	var spikes []CorrelatedSpike
	check := func(runner string, buckets []Bucket, threshold time.Duration) {
		byIndex := storageByIndex(buckets)
		for _, httpBucket := range result.HTTPResult.Buckets {
			index := httpBucket.Start.Unix()
			storageBucket, ok := byIndex[index]
			if !ok || storageBucket.P99 <= threshold {
				continue
			}
			spikes = append(spikes, CorrelatedSpike{
				BucketIndex:    index,
				Runner:         runner,
				HTTPLatency:    httpBucket.P99,
				StorageLatency: storageBucket.P99,
				Masked:         httpBucket.P99 <= httpThreshold,
			})
		}
	}
	if result.DBResult != nil {
		check("db", result.DBResult.Buckets, dbThreshold)
	}
	if result.RedisResult != nil {
		check("redis", result.RedisResult.Buckets, redisThreshold)
	}

	sort.SliceStable(spikes, func(i, j int) bool { return spikes[i].BucketIndex < spikes[j].BucketIndex })
	return CorrelationResult{Spikes: spikes}
}
