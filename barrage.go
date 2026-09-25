package barrage

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/codetesla51/barrage/internal/chaos"
	"golang.org/x/sync/errgroup"
)

type OrchestratorConfig struct {
	Duration    Duration           `yaml:"duration"`
	BucketWidth Duration           `yaml:"bucket_width"`
	Ramp        Duration           `yaml:"ramp"`
	Concurrency int                `yaml:"concurrency"`
	HTTP        *HTTPRunnerConfig  `yaml:"http"`
	DB          *DBRunnerConfig    `yaml:"db"`
	Redis       *RedisRunnerConfig `yaml:"redis"`
	Scenario    []Scenario         `yaml:"scenario"`
	// Chaos optionally injects Toxiproxy network faults during the run.
	// Nil means normal load test: zero effect on runner behavior.
	Chaos *ChaosConfig `yaml:"chaos,omitempty"`
	// Capacity configures a capacity sweep: raise concurrency level by level
	// until latency breaks. See capacity.go. Start comes from Concurrency.
	Capacity *CapacityConfig `yaml:"capacity"`
	// DeprecatedAutoRamp accepts the pre-0.6 name auto_ramp: for Capacity.
	// LoadConfig merges it in (and sets UsedDeprecatedAutoRamp) so existing
	// configs keep working while the new name takes over.
	DeprecatedAutoRamp *CapacityConfig `yaml:"auto_ramp"`
	// UsedDeprecatedAutoRamp is set by LoadConfig when the swept config came
	// in through the deprecated auto_ramp: key, so callers can print a rename
	// warning.
	UsedDeprecatedAutoRamp bool `yaml:"-"`
	// Deprecated: renamed from Scenarios (yaml "scenarios"). Friendly error only.
	DeprecatedScenarios []Scenario `yaml:"scenarios"`
	// Stats optionally supplies the live counters (shared with a caller-run
	// progress view). Nil creates private counters.
	Stats *RunStats `yaml:"-"`
	// Quiet skips the built-in stderr progress logger, for callers that
	// render their own (e.g. the Bubble Tea view). Final tables still print.
	Quiet bool `yaml:"-"`
}

type HTTPRunnerConfig struct {
	Target HTTPTarget `yaml:"target"`
	Rate   int        `yaml:"rate"`
}

type DBRunnerConfig struct {
	Target DBTarget `yaml:"target"`
	Rate   int      `yaml:"rate"`
}

type RedisRunnerConfig struct {
	Target RedisTarget `yaml:"target"`
	Rate   int         `yaml:"rate"`
}
type OrchestratorResult struct {
	HTTPResult         *HTTPResult
	DBResult           *DBResult
	RedisResult        *RedisResult
	ScenarioStats      *ScenarioStats
	ScenarioName       string
	ScenarioAggregates []NamedScenarioStats
	// ChaosEvents logs every fault injection/removal with a timestamp so the
	// correlation engine can plot it against latency/error buckets.
	ChaosEvents []ChaosEvent
}

func Orchestrator(cfg OrchestratorConfig) (*OrchestratorResult, error) {
	var group errgroup.Group
	var httpResult *HTTPResult
	var dbResult *DBResult
	var redisResult *RedisResult
	var scenarioStats *ScenarioStats
	var scenarioName string
	var scenarioAggregates []NamedScenarioStats
	var httpErr, dbErr, redisErr, scenarioErr error
	duration := time.Duration(cfg.Duration)
	bucketWidth := time.Duration(cfg.BucketWidth)
	ramp := time.Duration(cfg.Ramp)
	concurrency := cfg.Concurrency
	if bucketWidth <= 0 {
		bucketWidth = time.Second
	}
	runStats := cfg.Stats
	if runStats == nil {
		runStats = &RunStats{}
	}

	// Chaos setup: strictly opt-in. Nil Chaos means the exact historical path.
	chaosActive := cfg.Chaos != nil
	var chaosMgr *chaos.Manager
	var chaosEvents []ChaosEvent
	if chaosActive {
		if err := validateChaos(cfg.Chaos, duration); err != nil {
			return nil, err
		}
		// SQLite is a local file, not TCP — it cannot go through Toxiproxy.
		if cfg.DB != nil && NormalizeDriver(cfg.DB.Target.Driver) == "sqlite" && chaosHasDBFaults(cfg.Chaos) {
			return nil, fmt.Errorf("chaos: sqlite target cannot use network faults (local file, no TCP to proxy)")
		}
		api := cfg.Chaos.API
		if api == "" {
			api = chaos.DefaultAPIAddr
		}
		// Auto-spawn: use the running server when present, otherwise start
		// a managed toxiproxy-server and stop it after the run. The spawn
		// and the stop are both announced on stderr; cleanup is deferred so
		// no proxy process leaks even when a runner fails.
		mgr, cleanup, _, err := chaos.EnsureManager(api)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		chaosMgr = mgr
		for _, p := range cfg.Chaos.Proxies {
			if err := chaosMgr.CreateProxy(p.Name, p.Listen, p.Upstream); err != nil {
				return nil, err
			}
		}
	}

	done := make(chan struct{})
	if !cfg.Quiet {
		go runStats.StartLogger(done, duration) // logs to stderr every 5s until the run ends
	}

	// Resolve effective targets: chaos overrides (chaos_conn/addr/url) swap
	// the dial address when chaos mode is on; otherwise the real target.
	var httpTarget HTTPTarget
	var httpRate int
	var dbTarget DBTarget
	var dbRate int
	var redisTarget RedisTarget
	var redisRate int
	if cfg.HTTP != nil {
		httpTarget = cfg.HTTP.Target
		httpRate = cfg.HTTP.Rate
		if chaosActive {
			httpTarget.URL = effectiveHTTPURL(cfg.HTTP.Target, true)
		}
	}
	if cfg.DB != nil {
		dbTarget = cfg.DB.Target
		dbRate = cfg.DB.Rate
		if chaosActive {
			dbTarget.Conn = effectiveDBConn(cfg.DB.Target, true)
		}
	}
	if cfg.Redis != nil {
		redisTarget = cfg.Redis.Target
		redisRate = cfg.Redis.Rate
		if chaosActive {
			redisTarget.Addr = effectiveRedisAddr(cfg.Redis.Target, true)
		}
	}
	scenarios := effectiveScenarios(cfg)
	if chaosActive {
		scenarios = applyScenarioChaos(scenarios)
	}

	// Chaos scheduler ticks against the same start as the runners.
	var sched *Scheduler
	schedDone := make(chan []ChaosEvent, 1)
	var schedCtx context.Context
	var schedCancel context.CancelFunc
	if chaosActive {
		start := time.Now()
		sched = NewScheduler(chaosMgr, faultEvents(cfg.Chaos.Faults), start)
		schedCtx, schedCancel = context.WithCancel(context.Background())
		defer schedCancel()
		go func() {
			schedDone <- sched.Run(schedCtx)
		}()
	}

	if cfg.HTTP != nil {
		group.Go(func() error {
			var err error
			httpResult, err = FireHTTP(httpTarget, httpRate, concurrency, duration, bucketWidth, ramp, runStats)
			return err
		})
	}
	if cfg.DB != nil {
		group.Go(func() error {
			var err error
			dbResult, err = FireDB(dbTarget, dbRate, concurrency, duration, bucketWidth, ramp, runStats)
			return err
		})
	}
	if cfg.Redis != nil {
		group.Go(func() error {
			var err error
			redisResult, err = FireRedis(redisTarget, redisRate, concurrency, duration, bucketWidth, ramp, runStats)
			return err
		})
	}
	if len(scenarios) > 0 {
		group.Go(func() error {
			if len(scenarios) == 1 {
				stats, err := FireScenario(scenarios[0], concurrency, duration, bucketWidth, runStats)
				scenarioStats = stats
				scenarioErr = err
				name := scenarios[0].Name
				if name == "" {
					name = "scenario"
				}
				scenarioName = name
				if err == nil && stats != nil {
					scenarioAggregates = []NamedScenarioStats{{Name: name, Stats: stats}}
				}
				return err
			}

			aggs, err := FireScenarios(scenarios, concurrency, duration, bucketWidth, runStats)
			scenarioErr = err
			if err == nil {
				scenarioAggregates = aggs
				if len(aggs) > 0 {
					scenarioName = aggs[0].Name
					scenarioStats = aggs[0].Stats
				}
			}
			return err
		})
	}

	// Wait for every runner so one failing layer still contributes its full
	// measurement window. A plain errgroup does not cancel sibling runners.
	waitErr := group.Wait()
	if chaosActive {
		schedCancel()
		select {
		case chaosEvents = <-schedDone:
		case <-time.After(10 * time.Second):
			// Scheduler cleanup must not hang the run if Toxiproxy stalls.
			chaosEvents = sched.Events()
		}
		// Never leak toxics into the next run.
		_ = chaosMgr.Reset()
	}
	close(done)
	fmt.Fprintf(os.Stderr, "[barrage] done · %s\n", runStats.Summary())
	// Keep the existing HTTP -> DB -> Redis -> scenario error precedence.
	if httpErr != nil {
		return nil, httpErr
	}
	if dbErr != nil {
		return nil, dbErr
	}
	if redisErr != nil {
		return nil, redisErr
	}
	if scenarioErr != nil {
		return nil, scenarioErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return &OrchestratorResult{
		HTTPResult:         httpResult,
		DBResult:           dbResult,
		RedisResult:        redisResult,
		ScenarioStats:      scenarioStats,
		ScenarioName:       scenarioName,
		ScenarioAggregates: scenarioAggregates,
		ChaosEvents:        chaosEvents,
	}, nil
}

// chaosHasDBFaults reports whether any fault targets a proxy. The caller
// already knows the DB is sqlite (unproxyable), so any fault is a misconfig.
// We keep this conservative: sqlite + any chaos faults is rejected because the
// DB runner cannot go through TCP while other runners can.
func chaosHasDBFaults(c *ChaosConfig) bool {
	return c != nil && len(c.Faults) > 0
}

// applyScenarioChaos rewrites step URLs to their chaos overrides.
func applyScenarioChaos(scenarios []Scenario) []Scenario {
	out := make([]Scenario, len(scenarios))
	for i, s := range scenarios {
		steps := make([]Step, len(s.Steps))
		for j, st := range s.Steps {
			st.URL = effectiveStepURL(st, true)
			steps[j] = st
		}
		s.Steps = steps
		out[i] = s
	}
	return out
}
