package barrage

import (
	"sync"
	"time"
)

type OrchestratorConfig struct {
	Duration    Duration           `yaml:"duration"`
	BucketWidth Duration           `yaml:"bucket_width"`
	HTTP        *HTTPRunnerConfig  `yaml:"http"`
	DB          *DBRunnerConfig    `yaml:"db"`
	Redis       *RedisRunnerConfig `yaml:"redis"`
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
	HTTPResult  *HTTPResult
	DBResult    *DBResult
	RedisResult *RedisResult
}

func Orchestrator(cfg OrchestratorConfig) (*OrchestratorResult, error) {
	var wg sync.WaitGroup
	var httpResult *HTTPResult
	var dbResult *DBResult
	var redisResult *RedisResult
	var httpErr, dbErr, redisErr error
	if cfg.HTTP != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpResult, httpErr = FireHTTP(cfg.HTTP.Target, cfg.HTTP.Rate, time.Duration(cfg.Duration), time.Duration(cfg.BucketWidth))
		}()
	}
	if cfg.DB != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dbResult, dbErr = FireDB(cfg.DB.Target, cfg.DB.Rate, time.Duration(cfg.Duration), time.Duration(cfg.BucketWidth))
		}()
	}
	if cfg.Redis != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			redisResult, redisErr = FireRedis(cfg.Redis.Target, cfg.Redis.Rate, time.Duration(cfg.Duration), time.Duration(cfg.BucketWidth))
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
	return &OrchestratorResult{
		HTTPResult:  httpResult,
		DBResult:    dbResult,
		RedisResult: redisResult,
	}, nil
}
