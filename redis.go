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
// parameters.
func FireRedis(target RedisTarget, rate int, duration time.Duration, bucketWidth time.Duration) (*RedisResult, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     target.Addr,
		Password: target.Password,
		DB:       target.DB,
	})
	defer client.Close()

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	interval := time.Duration(int(time.Second) / rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timeout := time.After(duration)

	overall := []dbQueryResult{}
	runStart := time.Now()

Loop:
	for {
		select {
		case <-ticker.C:
			startTime := time.Now()
			cmd := pickQuery(cumulativeWeights(target.Query))
			err := client.Do(ctx, splitCommand(cmd)...).Err()
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

// splitCommand splits a Redis command string into its arguments for client.Do.
func splitCommand(s string) []interface{} {
	fields := strings.Fields(s)
	args := make([]interface{}, len(fields))
	for i, f := range fields {
		args[i] = f
	}
	return args
}
