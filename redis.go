package barrage

import (
	"context"
	"errors"
	"fmt"
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
	return fireRedis(client, target, rate, concurrency, duration, bucketWidth, ramp, stats)
}

// fireRedis is the shared burst core: the caller owns client, so normal
// runs and capacity-sweep levels execute the exact same command path.
// A future change here fixes both at once.
func fireRedis(client *redis.Client, target RedisTarget, rate, concurrency int, duration, bucketWidth, ramp time.Duration, stats *RunStats) (*DBResult, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	// cancellable: runPaced cancels this at the deadline so in-flight commands
	// abort instead of blocking shutdown on a wedged redis
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	// runPaced cancels its context at the deadline before draining the pool,
	// so in-flight commands abort instead of blocking shutdown. The closure
	// names that context `runCtx` (the client-level ctx at the top is separate
	// and stays live until fireRedis returns — checking it would never fire).
	overall, start := runPaced(rate, concurrency, duration, ramp, func(runCtx context.Context) dbQueryResult {
		pick := pickQuery(cumulativeWeights(target.Query))
		queryStart := time.Now()
		// per-op timeout so a stalled connection can't hang past the deadline
		opCtx, opCancel := context.WithTimeout(runCtx, 10*time.Second)
		defer opCancel()
		err := client.Do(opCtx, splitCommand(pick.Query)...).Err()
		if err != nil && errors.Is(err, context.Canceled) {
			// aborted by run shutdown before the server answered: the op
			// never ran, so it is not a target failure. Any other error means
			// the server responded and surfaces as before.
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
