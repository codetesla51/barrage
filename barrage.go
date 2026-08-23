package barrage

import (
	"sync"
	"time"
)

type OrchestratorConfig struct {
	Duration    Duration           `yaml:"duration"`
	BucketWidth Duration           `yaml:"bucket_width"`
	Ramp        Duration           `yaml:"ramp"`
	Concurrency int                `yaml:"concurrency"`
	HTTP        *HTTPRunnerConfig  `yaml:"http"`
	DB          *DBRunnerConfig    `yaml:"db"`
	Redis       *RedisRunnerConfig `yaml:"redis"`
	Scenarios   []Scenario         `yaml:"scenarios"`
	// Deprecated: use Scenarios. Kept to give a helpful error if someone writes 'scenario:'.
	DeprecatedScenario *Scenario `yaml:"scenario"`
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
	HTTPResult           *HTTPResult
	DBResult             *DBResult
	RedisResult          *RedisResult
	ScenarioStats        *ScenarioStats
	ScenarioName         string
	ScenarioAggregates   []NamedScenarioStats
}

func Orchestrator(cfg OrchestratorConfig) (*OrchestratorResult, error) {
	return orchestrate(cfg, nil)
}

// OrchestratorWithProgress runs the same load test as Orchestrator but records
// each completed hit into prog so the CLI can render live progress. The
// returned metrics are identical to Orchestrator's.
func OrchestratorWithProgress(cfg OrchestratorConfig, prog *RunProgress) (*OrchestratorResult, error) {
	return orchestrate(cfg, prog)
}

func orchestrate(cfg OrchestratorConfig, prog *RunProgress) (*OrchestratorResult, error) {
	var wg sync.WaitGroup
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
	if cfg.HTTP != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpResult, httpErr = FireHTTP(cfg.HTTP.Target, cfg.HTTP.Rate, concurrency, duration, bucketWidth, ramp, prog)
		}()
	}
	if cfg.DB != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dbResult, dbErr = FireDB(cfg.DB.Target, cfg.DB.Rate, concurrency, duration, bucketWidth, ramp)
		}()
	}
	if cfg.Redis != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			redisResult, redisErr = FireRedis(cfg.Redis.Target, cfg.Redis.Rate, concurrency, duration, bucketWidth, ramp, prog)
		}()
	}
	scenarios := effectiveScenarios(cfg)
	if len(scenarios) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if len(scenarios) == 1 {
				stats, err := FireScenario(scenarios[0], concurrency, duration, bucketWidth, prog)
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
			} else {
				aggs, err := FireScenarios(scenarios, concurrency, duration, bucketWidth, prog)
				scenarioErr = err
				if err == nil {
					scenarioAggregates = aggs
					if len(aggs) > 0 {
						scenarioName = aggs[0].Name
						scenarioStats = aggs[0].Stats
					}
				}
			}
		}()
	}
	wg.Wait()
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
	return &OrchestratorResult{
		HTTPResult:         httpResult,
		DBResult:           dbResult,
		RedisResult:        redisResult,
		ScenarioStats:      scenarioStats,
		ScenarioName:       scenarioName,
		ScenarioAggregates: scenarioAggregates,
	}, nil
}
