package barrage

import (
	"reflect"
	"testing"
	"time"
)

func TestCorrelate(t *testing.T) {
	base := time.Unix(1700000000, 0)

	tests := []struct {
		name           string
		http           []HTTPBucket
		db             []Bucket
		redis          []Bucket
		httpThreshold  time.Duration
		dbThreshold    time.Duration
		redisThreshold time.Duration
		want           []CorrelatedSpike
	}{
		{
			name: "no overlapping bucket indices",
			http: []HTTPBucket{
				{Start: base, P99: 200 * time.Millisecond},
				{Start: base.Add(time.Second), P99: 200 * time.Millisecond},
			},
			db: []Bucket{
				{Start: base.Add(2 * time.Second).Unix(), P99: 200 * time.Millisecond},
				{Start: base.Add(3 * time.Second).Unix(), P99: 200 * time.Millisecond},
			},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want:           nil,
		},
		{
			name:           "overlapping, neither crosses threshold",
			http:           []HTTPBucket{{Start: base, P99: 50 * time.Millisecond}},
			db:             []Bucket{{Start: base.Unix(), P99: 60 * time.Millisecond}},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want:           nil,
		},
		{
			name:           "overlapping, only http crosses threshold",
			http:           []HTTPBucket{{Start: base, P99: 200 * time.Millisecond}},
			db:             []Bucket{{Start: base.Unix(), P99: 50 * time.Millisecond}},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want:           nil,
		},
		{
			name:           "overlapping, only db crosses threshold (masked)",
			http:           []HTTPBucket{{Start: base, P99: 50 * time.Millisecond}},
			db:             []Bucket{{Start: base.Unix(), P99: 200 * time.Millisecond}},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want: []CorrelatedSpike{
				{BucketIndex: base.Unix(), Runner: "db", HTTPLatency: 50 * time.Millisecond, StorageLatency: 200 * time.Millisecond, Masked: true},
			},
		},
		{
			name:           "overlapping, both cross threshold",
			http:           []HTTPBucket{{Start: base, P99: 200 * time.Millisecond}},
			db:             []Bucket{{Start: base.Unix(), P99: 300 * time.Millisecond}},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want: []CorrelatedSpike{
				{BucketIndex: base.Unix(), Runner: "db", HTTPLatency: 200 * time.Millisecond, StorageLatency: 300 * time.Millisecond},
			},
		},
		{
			name: "bucket present on one side only is skipped",
			http: []HTTPBucket{
				{Start: base, P99: 200 * time.Millisecond},                      // no db match
				{Start: base.Add(time.Second), P99: 200 * time.Millisecond},     // matches db
				{Start: base.Add(2 * time.Second), P99: 200 * time.Millisecond}, // no db match
			},
			db: []Bucket{
				{Start: base.Add(time.Second).Unix(), P99: 300 * time.Millisecond},     // matches http
				{Start: base.Add(3 * time.Second).Unix(), P99: 300 * time.Millisecond}, // no http match
			},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want: []CorrelatedSpike{
				{BucketIndex: base.Add(time.Second).Unix(), Runner: "db", HTTPLatency: 200 * time.Millisecond, StorageLatency: 300 * time.Millisecond},
			},
		},
		{
			name:           "redis spike without db",
			http:           []HTTPBucket{{Start: base, P99: 50 * time.Millisecond}},
			redis:          []Bucket{{Start: base.Unix(), P99: 500 * time.Millisecond}},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want: []CorrelatedSpike{
				{BucketIndex: base.Unix(), Runner: "redis", HTTPLatency: 50 * time.Millisecond, StorageLatency: 500 * time.Millisecond, Masked: true},
			},
		},
		{
			name: "db and redis spike in same bucket",
			http: []HTTPBucket{
				{Start: base, P99: 200 * time.Millisecond},
				{Start: base.Add(2 * time.Second), P99: 300 * time.Millisecond},
			},
			db: []Bucket{{Start: base.Unix(), P99: 250 * time.Millisecond}},
			redis: []Bucket{
				{Start: base.Unix(), P99: 400 * time.Millisecond},
				{Start: base.Add(2 * time.Second).Unix(), P99: 350 * time.Millisecond},
			},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want: []CorrelatedSpike{
				{BucketIndex: base.Unix(), Runner: "db", HTTPLatency: 200 * time.Millisecond, StorageLatency: 250 * time.Millisecond},
				{BucketIndex: base.Unix(), Runner: "redis", HTTPLatency: 200 * time.Millisecond, StorageLatency: 400 * time.Millisecond},
				{BucketIndex: base.Add(2 * time.Second).Unix(), Runner: "redis", HTTPLatency: 300 * time.Millisecond, StorageLatency: 350 * time.Millisecond},
			},
		},
		{
			name:           "redis under its threshold is not flagged",
			http:           []HTTPBucket{{Start: base, P99: 200 * time.Millisecond}},
			db:             []Bucket{{Start: base.Unix(), P99: 300 * time.Millisecond}},
			redis:          []Bucket{{Start: base.Unix(), P99: 20 * time.Millisecond}},
			httpThreshold:  100 * time.Millisecond,
			dbThreshold:    100 * time.Millisecond,
			redisThreshold: 100 * time.Millisecond,
			want: []CorrelatedSpike{
				{BucketIndex: base.Unix(), Runner: "db", HTTPLatency: 200 * time.Millisecond, StorageLatency: 300 * time.Millisecond},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &OrchestratorResult{
				HTTPResult: &HTTPResult{Buckets: tt.http},
			}
			if tt.db != nil {
				result.DBResult = &DBResult{Buckets: tt.db}
			}
			if tt.redis != nil {
				result.RedisResult = &RedisResult{Buckets: tt.redis}
			}
			got := Correlate(result, tt.httpThreshold, tt.dbThreshold, tt.redisThreshold)
			if !reflect.DeepEqual(got.Spikes, tt.want) {
				t.Errorf("Correlate() spikes = %v, want %v", got.Spikes, tt.want)
			}
		})
	}
}

func TestCorrelateNilInputs(t *testing.T) {
	if got := Correlate(nil, time.Millisecond, time.Millisecond, time.Millisecond); got.Spikes != nil {
		t.Errorf("nil result: spikes = %v, want nil", got.Spikes)
	}
	if got := Correlate(&OrchestratorResult{}, time.Millisecond, time.Millisecond, time.Millisecond); got.Spikes != nil {
		t.Errorf("empty result: spikes = %v, want nil", got.Spikes)
	}
}
