package barrage

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisTarget represents a Redis target for load testing. Query holds weighted
// Redis command strings (e.g. "PING", "GET mykey") to pick from per tick.
type RedisTarget struct {
	Addr     string        `yaml:"addr"`
	Password string        `yaml:"password"`
	DB       int           `yaml:"db"`
	Query    []QueryWeight `yaml:"queries"`
}

// RedisResult is the run summary for a Redis load test. It reuses the same
// aggregation shape as the database runner.
type RedisResult = DBResult

// FireRedis executes Redis commands according to the specified target and
// parameters. Commands are fired at rate per second (ramping up over ramp if
// set) and run concurrently on a worker pool with up to concurrency workers.
func FireRedis(target RedisTarget, rate, concurrency int, duration, bucketWidth, ramp time.Duration, stats *RunStats) (*DBResult, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     target.Addr,
		Password: target.Password,
		DB:       target.DB,
	})
	defer client.Close()
	// cancellable: runPaced cancels this at the deadline so in-flight commands
	// abort instead of blocking shutdown on a wedged redis
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	overall, start := runPaced(rate, concurrency, duration, ramp, func(opCtx context.Context) dbQueryResult {
		pick := pickQuery(cumulativeWeights(target.Query))
		queryStart := time.Now()
		// per-op timeout so a stalled connection can't hang past the deadline
		opCtx, opCancel := context.WithTimeout(opCtx, 10*time.Second)
		defer opCancel()
		err := client.Do(opCtx, splitCommand(pick.Query)...).Err()
		if err != nil && opCtx.Err() != nil && ctx.Err() != nil {
			// canceled by run shutdown, not a target failure
			return dbQueryResult{Latency: time.Since(queryStart), Success: true}
		}
		if stats != nil {
			stats.RedisFired.Add(1)
			if err != nil {
				stats.RedisErr.Add(1)
			}
		}
		return dbQueryResult{Latency: time.Since(queryStart), Success: err == nil, Err: err}
	})

	return buildDBResult(overall, start, bucketWidth, duration), nil
}

// splitCommand splits a Redis command string into its arguments for client.Do.
func splitCommand(s string) []interface{} {
	fields := strings.Fields(s)
	args := make([]interface{}, len(fields))
	for i, f := range fields {
		args[i] = f
	}
	return args
}
