package barrage

import (
	"sync"
	"time"
)

type OrchestratorConfig struct {
	Duration    time.Duration
	BucketWidth time.Duration
	HTTP        HTTPRunnerConfig
	DB          DBRunnerConfig
}

type HTTPRunnerConfig struct {
	Target HTTPTarget
	Rate   int
}

type DBRunnerConfig struct {
	Target DBTarget
	Rate   int
}
type OrchestratorResult struct {
	HTTPResult *HTTPResult
	DBResult   *DBResult
}

func Orchestrator(cfg OrchestratorConfig) (*OrchestratorResult, error) {
	var wg sync.WaitGroup
	var httpResult *HTTPResult
	var dbResult *DBResult
	var httpErr, dbErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		httpResult, httpErr = FireHTTP(cfg.HTTP.Target, cfg.HTTP.Rate, cfg.Duration, cfg.BucketWidth)
		if httpErr != nil {
			return
		}
	}()
	go func() {
		defer wg.Done()
		dbResult, dbErr = FireDB(cfg.DB.Target, cfg.DB.Rate, cfg.Duration, cfg.BucketWidth)
		if dbErr != nil {
			return
		}
	}()
	wg.Wait()
	if httpErr != nil {
		return nil, httpErr
	}
	if dbErr != nil {
		return nil, dbErr
	}
	return &OrchestratorResult{
		HTTPResult: httpResult,
		DBResult:   dbResult,
	}, nil
}
