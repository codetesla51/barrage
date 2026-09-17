package barrage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultRampStepDuration is how long each concurrency level runs.
// Shorter than a normal run duration on purpose: just enough buckets
// to see if latency holds, without paying a full run per level.
const DefaultRampStepDuration = 10 * time.Second

// minRampSuccess marks a burst as broken when success drops below it,
// even if P99 stayed under threshold (e.g. errors instead of slowness).
const minRampSuccess = 0.95

// AutoRampConfig ramps concurrency to find where latency breaks.
// Start comes from the run's concurrency; Max caps the search.
// StepDuration is per-level burst time (default 10s).
type AutoRampConfig struct {
	MaxConcurrency int      `yaml:"max_concurrency"`
	StepDuration   Duration `yaml:"step_duration"`
}

// RampStep is one concurrency level that ran.
type RampStep struct {
	Concurrency int
	Requests    uint64
	P99         time.Duration // worst P99 across runners that ran
	Success     float64       // 0-1, worst success across runners that ran
	Broken      bool
	HTTPP99     time.Duration
	DBP99       time.Duration
	RedisP99    time.Duration
	ScenarioP99 time.Duration
}

// RampResult is the whole search: one point per level plus the verdict.
type RampResult struct {
	Steps   []RampStep
	BreakAt int // first broken concurrency, 0 = held to max
	LastOK  int // highest concurrency that held
}

// effectiveRamp resolves defaults for a ramp search.
func effectiveRamp(cfg OrchestratorConfig, ramp AutoRampConfig) (start, max int, stepDur, bucketWidth time.Duration) {
	start = cfg.Concurrency
	if start <= 0 {
		start = DefaultConcurrency
	}
	max = ramp.MaxConcurrency
	if max <= 0 {
		max = start * 4
	}
	stepDur = time.Duration(ramp.StepDuration)
	if stepDur <= 0 {
		stepDur = DefaultRampStepDuration
	}
	bucketWidth = time.Duration(cfg.BucketWidth)
	if bucketWidth <= 0 {
		bucketWidth = time.Second
	}
	return start, max, stepDur, bucketWidth
}

// planCoarseLevels doubles from start to max so the rough zone is found
// fast: 10, 20, 40, 80, 160. Max is always included as the final level
// even when it is not on the doubling grid.
func planCoarseLevels(start, max int) []int {
	if start <= 0 {
		start = DefaultConcurrency
	}
	if max < start {
		return []int{start}
	}
	var out []int
	for c := start; c < max; c *= 2 {
		out = append(out, c)
		if c > max/2 {
			break
		}
	}
	if len(out) == 0 || out[len(out)-1] != max {
		out = append(out, max)
	}
	return out
}

// planFineLevels fills linearly between the last OK level and the first
// broken one, so doubling's coarse bracket gets pinned down:
// ok=80 broken=160 -> 100, 120, 140.
func planFineLevels(ok, broken int) []int {
	gap := broken - ok
	if gap <= 1 {
		return nil
	}
	step := gap / 4
	if step < 1 {
		step = 1
	}
	var out []int
	for c := ok + step; c < broken; c += step {
		out = append(out, c)
	}
	return out
}

// scaledRate keeps per-worker load constant while concurrency grows,
// so raising concurrency actually raises total load for the paced
// runners (http/db/redis). Scenario load comes from VUs, not rate.
func scaledRate(baseRate, baseConc, stepConc int) int {
	if baseRate <= 0 {
		return baseRate
	}
	if baseConc <= 0 {
		baseConc = DefaultConcurrency
	}
	r := baseRate * stepConc / baseConc
	if r < 1 {
		return 1
	}
	return r
}

// rampStepBroken reports whether a burst counts as broken: any runner's
// P99 over its threshold, or success below the floor.
func rampStepBroken(httpP99, dbP99, redisP99, scenP99 time.Duration, httpOK, dbOK, redisOK, scenOK bool, success float64, httpTh, dbTh, redisTh time.Duration) bool {
	if success < minRampSuccess {
		return true
	}
	if httpOK && httpP99 > httpTh {
		return true
	}
	if dbOK && dbP99 > dbTh {
		return true
	}
	if redisOK && redisP99 > redisTh {
		return true
	}
	if scenOK && scenP99 > httpTh {
		return true
	}
	return false
}

// RunAutoRamp searches concurrency for the first level where latency
// breaks. DB and Redis connections open once up front (sized to max)
// and stay alive across levels — only the worker count changes per
// burst, so early-bucket slowness is real strain, not reconnect cost.
func RunAutoRamp(cfg OrchestratorConfig, ramp AutoRampConfig, httpTh, dbTh, redisTh time.Duration) (*RampResult, error) {
	start, max, stepDur, bucketWidth := effectiveRamp(cfg, ramp)
	if max < start {
		return nil, fmt.Errorf("auto_ramp max_concurrency %d below start %d", max, start)
	}
	if httpTh <= 0 {
		httpTh = 100 * time.Millisecond
	}
	if dbTh <= 0 {
		dbTh = 100 * time.Millisecond
	}
	if redisTh <= 0 {
		redisTh = 100 * time.Millisecond
	}
	stats := cfg.Stats
	if stats == nil {
		stats = &RunStats{}
	}

	var db *sql.DB
	if cfg.DB != nil {
		var err error
		db, err = OpenConnection(cfg.DB.Target.Conn, cfg.DB.Target.Driver)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		// Size to max once so later levels reuse warm connections.
		applyPoolOptions(db, cfg.DB.Target, max)
	}

	var rdb *redis.Client
	if cfg.Redis != nil {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Target.Addr,
			Password: cfg.Redis.Target.Password,
			DB:       cfg.Redis.Target.DB,
		})
		defer rdb.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := rdb.Ping(ctx).Err(); err != nil {
			cancel()
			return nil, err
		}
		cancel()
	}

	res := &RampResult{}
	seen := make(map[int]bool)
	lastOK := 0
	var firstBroken int

	runLevel := func(conc int) (*RampStep, error) {
		if seen[conc] {
			return nil, nil
		}
		seen[conc] = true
		fmt.Fprintf(os.Stderr, "[ramp] concurrency %d · step %s\n", conc, stepDur)
		step, err := runRampBurst(cfg, db, rdb, conc, start, stepDur, bucketWidth, stats, httpTh, dbTh, redisTh)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "[ramp]   → p99 %s success %.1f%% %s\n", step.P99, step.Success*100, rampVerdict(step.Broken))
		res.Steps = append(res.Steps, *step)
		if step.Broken && firstBroken == 0 {
			firstBroken = conc
		}
		if !step.Broken {
			lastOK = conc
		}
		return step, nil
	}

	for _, conc := range planCoarseLevels(start, max) {
		step, err := runLevel(conc)
		if err != nil {
			return nil, err
		}
		if step.Broken {
			break
		}
	}
	if firstBroken != 0 && firstBroken-lastOK > 1 {
		for _, conc := range planFineLevels(lastOK, firstBroken) {
			if _, err := runLevel(conc); err != nil {
				return nil, err
			}
		}
	}

	res.BreakAt = firstBroken
	// Fine levels may have broken earlier than the coarse bracket.
	for _, s := range res.Steps {
		if s.Broken && (res.BreakAt == 0 || s.Concurrency < res.BreakAt) {
			res.BreakAt = s.Concurrency
		}
		if !s.Broken && s.Concurrency > res.LastOK {
			res.LastOK = s.Concurrency
		}
	}
	if res.LastOK == 0 {
		res.LastOK = lastOK
	}
	return res, nil
}

// fireDBWithDB is FireDB without open/close: the caller owns db so the
// pool stays warm across bursts and only the worker count varies.
func fireDBWithDB(db *sql.DB, target DBTarget, rate, concurrency int, duration, bucketWidth time.Duration, stats *RunStats) (*DBResult, error) {
	if db == nil {
		return nil, fmt.Errorf("db handle is nil")
	}
	overall, start := runPaced(rate, concurrency, duration, 0, func(ctx context.Context) dbQueryResult {
		pick := pickQuery(cumulativeWeights(target.Query))
		queryStart := time.Now()
		opCtx, opCancel := context.WithTimeout(ctx, 10*time.Second)
		defer opCancel()
		var err error
		if queryIsRead(pick) {
			var rows *sql.Rows
			rows, err = db.QueryContext(opCtx, pick.Query, pick.Args...)
			if err == nil {
				rows.Close()
			}
		} else {
			_, err = db.ExecContext(opCtx, pick.Query, pick.Args...)
		}
		if err != nil && ctx.Err() != nil {
			return dbQueryResult{Latency: time.Since(queryStart), Success: true}
		}
		if stats != nil {
			stats.DBFired.Add(1)
			if err != nil {
				stats.DBErr.Add(1)
			}
		}
		return dbQueryResult{Latency: time.Since(queryStart), Success: err == nil, Err: err}
	})
	return buildDBResult(overall, start, bucketWidth, duration), nil
}

// fireRedisWithClient is FireRedis without open/close: the caller owns
// the client so connections stay warm across bursts.
func fireRedisWithClient(client *redis.Client, target RedisTarget, rate, concurrency int, duration, bucketWidth time.Duration, stats *RunStats) (*DBResult, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	overall, start := runPaced(rate, concurrency, duration, 0, func(opCtx context.Context) dbQueryResult {
		pick := pickQuery(cumulativeWeights(target.Query))
		queryStart := time.Now()
		opCtx, opCancel := context.WithTimeout(opCtx, 10*time.Second)
		defer opCancel()
		err := client.Do(opCtx, splitCommand(pick.Query)...).Err()
		if err != nil && opCtx.Err() != nil {
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

// rampVerdict renders a step outcome for the stderr progress line.
func rampVerdict(broken bool) string {
	if broken {
		return "broken"
	}
	return "ok"
}

// runRampBurst runs every configured runner once at conc for stepDur.
// Rates scale with conc so the paced runners push more total load;
// the inner ramp is off (0) since the burst itself is already short.
func runRampBurst(cfg OrchestratorConfig, db *sql.DB, rdb *redis.Client, conc, baseConc int, stepDur, bucketWidth time.Duration, stats *RunStats, httpTh, dbTh, redisTh time.Duration) (*RampStep, error) {
	var wg sync.WaitGroup
	var httpP99, dbP99, redisP99, scenP99 time.Duration
	var httpReq, dbReq, redisReq, scenReq uint64
	var httpSucc, dbSucc, redisSucc, scenSucc float64
	var httpOK, dbOK, redisOK, scenOK bool
	var firstErr error
	var mu sync.Mutex
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}

	if cfg.HTTP != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rate := scaledRate(cfg.HTTP.Rate, baseConc, conc)
			r, err := FireHTTP(cfg.HTTP.Target, rate, conc, stepDur, bucketWidth, 0, stats)
			if err != nil {
				fail(err)
				return
			}
			mu.Lock()
			httpP99, httpReq, httpSucc, httpOK = r.P99, r.Requests, r.Success, true
			mu.Unlock()
		}()
	}
	if cfg.DB != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rate := scaledRate(cfg.DB.Rate, baseConc, conc)
			r, err := fireDBWithDB(db, cfg.DB.Target, rate, conc, stepDur, bucketWidth, stats)
			if err != nil {
				fail(err)
				return
			}
			mu.Lock()
			dbP99, dbReq, dbSucc, dbOK = r.P99, r.Requests, r.Success, true
			mu.Unlock()
		}()
	}
	if cfg.Redis != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rate := scaledRate(cfg.Redis.Rate, baseConc, conc)
			r, err := fireRedisWithClient(rdb, cfg.Redis.Target, rate, conc, stepDur, bucketWidth, stats)
			if err != nil {
				fail(err)
				return
			}
			mu.Lock()
			redisP99, redisReq, redisSucc, redisOK = r.P99, r.Requests, r.Success, true
			mu.Unlock()
		}()
	}
	if len(effectiveScenarios(cfg)) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scenarios := effectiveScenarios(cfg)
			if len(scenarios) == 1 {
				st, err := FireScenario(scenarios[0], conc, stepDur, bucketWidth, stats)
				if err != nil {
					fail(err)
					return
				}
				mu.Lock()
				scenP99, scenReq, scenSucc, scenOK = st.P99, st.Requests, st.Success, true
				mu.Unlock()
				return
			}
			aggs, err := FireScenarios(scenarios, conc, stepDur, bucketWidth, stats)
			if err != nil {
				fail(err)
				return
			}
			var total uint64
			var worst time.Duration
			var wsum float64
			for _, a := range aggs {
				if a.Stats == nil {
					continue
				}
				total += a.Stats.Requests
				wsum += a.Stats.Success * float64(a.Stats.Requests)
				if a.Stats.P99 > worst {
					worst = a.Stats.P99
				}
			}
			mu.Lock()
			scenP99, scenReq, scenOK = worst, total, len(aggs) > 0
			if total > 0 {
				scenSucc = wsum / float64(total)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	step := &RampStep{
		Concurrency: conc,
		HTTPP99:     httpP99,
		DBP99:       dbP99,
		RedisP99:    redisP99,
		ScenarioP99: scenP99,
	}
	var total uint64
	worst := time.Duration(0)
	best := 1.0
	any := false
	consider := func(ok bool, req uint64, p99 time.Duration, succ float64) {
		if !ok {
			return
		}
		any = true
		total += req
		if p99 > worst {
			worst = p99
		}
		if succ < best {
			best = succ
		}
	}
	consider(httpOK, httpReq, httpP99, httpSucc)
	consider(dbOK, dbReq, dbP99, dbSucc)
	consider(redisOK, redisReq, redisP99, redisSucc)
	consider(scenOK, scenReq, scenP99, scenSucc)
	step.Requests = total
	step.P99 = worst
	step.Success = best
	if !any {
		step.Success = 0
	}
	step.Broken = rampStepBroken(httpP99, dbP99, redisP99, scenP99, httpOK, dbOK, redisOK, scenOK, step.Success, httpTh, dbTh, redisTh)
	return step, nil
}
