package barrage

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultCapacityStepDuration is how long each concurrency level runs.
// Shorter than a normal run duration on purpose: just enough buckets
// to see if latency holds, without paying a full run per level.
const DefaultCapacityStepDuration = 10 * time.Second

// minLevelSuccess marks a burst as broken when success drops below it,
// even if P99 stayed under threshold (e.g. errors instead of slowness).
const minLevelSuccess = 0.95

// CapacityConfig configures the capacity sweep: it raises concurrency to
// find where latency breaks. Start comes from the run's concurrency; Max
// caps the search. StepDuration is per-level burst time (default 10s).
type CapacityConfig struct {
	MaxConcurrency int      `yaml:"max_concurrency"`
	StepDuration   Duration `yaml:"step_duration"`
}

// CapacityStep is one concurrency level that ran.
type CapacityStep struct {
	Concurrency int
	Requests    uint64
	P99         time.Duration // worst P99 across runners that ran
	Success     float64       // 0-1, worst success across runners that ran
	Broken      bool
	BrokenBy    []string // runners that broke the level, e.g. ["db"]
	HTTPP99     time.Duration
	DBP99       time.Duration
	RedisP99    time.Duration
	ScenarioP99 time.Duration
}

// CapacityResult is the whole sweep: one point per level plus the verdict.
type CapacityResult struct {
	Steps   []CapacityStep
	BreakAt int // first broken concurrency, 0 = held to max
	LastOK  int // highest concurrency that held
}

// effectiveCapacity resolves defaults for a capacity sweep.
func effectiveCapacity(cfg OrchestratorConfig, sweep CapacityConfig) (start, max int, stepDur, bucketWidth time.Duration) {
	start = cfg.Concurrency
	if start <= 0 {
		start = DefaultConcurrency
	}
	max = sweep.MaxConcurrency
	if max <= 0 {
		max = start * 4
	}
	stepDur = time.Duration(sweep.StepDuration)
	if stepDur <= 0 {
		stepDur = DefaultCapacityStepDuration
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

// capacityBreakers names the runners that broke a level: P99 over threshold
// or success below the floor, checked per runner. Empty means the level
// held. The scenario runner is judged against the HTTP threshold since it
// is the app-side load.
func capacityBreakers(httpP99, dbP99, redisP99, scenP99 time.Duration, httpSucc, dbSucc, redisSucc, scenSucc float64, httpOK, dbOK, redisOK, scenOK bool, httpTh, dbTh, redisTh time.Duration) []string {
	var out []string
	if httpOK && (httpP99 > httpTh || httpSucc < minLevelSuccess) {
		out = append(out, "http")
	}
	if dbOK && (dbP99 > dbTh || dbSucc < minLevelSuccess) {
		out = append(out, "db")
	}
	if redisOK && (redisP99 > redisTh || redisSucc < minLevelSuccess) {
		out = append(out, "redis")
	}
	if scenOK && (scenP99 > httpTh || scenSucc < minLevelSuccess) {
		out = append(out, "scenario")
	}
	return out
}

// RunCapacitySweep raises concurrency level by level until latency breaks.
// DB and Redis connections open once up front (sized to max) and stay alive
// across levels — only the worker count changes per burst, so early-bucket
// slowness is real strain, not reconnect cost.
func RunCapacitySweep(cfg OrchestratorConfig, sweep CapacityConfig, httpTh, dbTh, redisTh time.Duration) (*CapacityResult, error) {
	start, max, stepDur, bucketWidth := effectiveCapacity(cfg, sweep)
	if max < start {
		return nil, fmt.Errorf("capacity max_concurrency %d below start concurrency %d", max, start)
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

	// Both handles open once and stay alive across levels: the pool
	// holds warm connections while only the per-burst worker count varies.
	// fireRedis pings on the first burst, so a bad addr still fails fast.
	var rdb *redis.Client
	if cfg.Redis != nil {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Target.Addr,
			Password: cfg.Redis.Target.Password,
			DB:       cfg.Redis.Target.DB,
		})
		defer rdb.Close()
	}

	res := &CapacityResult{}
	seen := make(map[int]bool)
	lastOK := 0
	var firstBroken int

	runLevel := func(conc int) (*CapacityStep, error) {
		if seen[conc] {
			return nil, nil
		}
		seen[conc] = true
		fmt.Fprintf(os.Stderr, "[capacity] concurrency %d · step %s\n", conc, stepDur)
		step, err := runCapacityStep(cfg, db, rdb, conc, start, stepDur, bucketWidth, stats, httpTh, dbTh, redisTh)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "[capacity]   → p99 %s success %.1f%% %s\n", step.P99, step.Success*100, levelVerdict(step.Broken))
		res.Steps = append(res.Steps, *step)
		// Track the tightest bracket: lowest broken, highest ok. The == 0
		// guard would freeze firstBroken at the coarse break and the loop
		// would re-probe seen levels forever instead of converging.
		if step.Broken && (firstBroken == 0 || conc < firstBroken) {
			firstBroken = conc
		}
		if !step.Broken && conc > lastOK {
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
	// Keep filling the shrinking bracket until ok and broken are adjacent.
	// One pass can straddle the knee (step 2 probes 12,14,16,18 but never
	// 11); each new level sits strictly inside the bracket, so one bound
	// moves inward every round and this always terminates.
	for firstBroken != 0 && firstBroken-lastOK > 1 {
		progress := false
		for _, conc := range planFineLevels(lastOK, firstBroken) {
			step, err := runLevel(conc)
			if err != nil {
				return nil, err
			}
			if step != nil {
				progress = true
			}
		}
		if !progress {
			break
		}
	}

	// Verdict from all steps: lowest broken level, and highest ok level
	// strictly below it (a flaky ok above the break is noise, not safety).
	for _, s := range res.Steps {
		if s.Broken && (res.BreakAt == 0 || s.Concurrency < res.BreakAt) {
			res.BreakAt = s.Concurrency
		}
	}
	for _, s := range res.Steps {
		if !s.Broken && (res.BreakAt == 0 || s.Concurrency < res.BreakAt) && s.Concurrency > res.LastOK {
			res.LastOK = s.Concurrency
		}
	}
	return res, nil
}

// levelVerdict renders a step outcome for the stderr progress line.
func levelVerdict(broken bool) string {
	if broken {
		return "broken"
	}
	return "ok"
}

// runCapacityStep runs every configured runner once at conc for stepDur.
// Rates scale with conc so the paced runners push more total load;
// the inner rate ramp is off (0) since the burst itself is already short.
func runCapacityStep(cfg OrchestratorConfig, db *sql.DB, rdb *redis.Client, conc, baseConc int, stepDur, bucketWidth time.Duration, stats *RunStats, httpTh, dbTh, redisTh time.Duration) (*CapacityStep, error) {
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
			r, err := fireDB(db, cfg.DB.Target, rate, conc, stepDur, bucketWidth, 0, stats)
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
			r, err := fireRedis(rdb, cfg.Redis.Target, rate, conc, stepDur, bucketWidth, 0, stats)
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

	step := &CapacityStep{
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
	step.BrokenBy = capacityBreakers(httpP99, dbP99, redisP99, scenP99, httpSucc, dbSucc, redisSucc, scenSucc, httpOK, dbOK, redisOK, scenOK, httpTh, dbTh, redisTh)
	step.Broken = len(step.BrokenBy) > 0
	return step, nil
}
