package barrage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// StepResult is the result of one step inside a scenario.
type StepResult struct {
	Method     string
	URL        string
	StatusCode int
	Duration   time.Duration
	Body       []byte
	Err        error
}

// Request is a single HTTP request for a scenario step.
type Request struct {
	Method  string
	URL     string
	Body    string
	Headers map[string]string
}

// HTTPRunner executes HTTP requests for scenarios.
type HTTPRunner struct {
	client *http.Client
}

// NewHTTPRunner creates a runner with a default client.
// If client is nil, http.DefaultClient is used.
func NewHTTPRunner(client *http.Client) *HTTPRunner {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPRunner{client: client}
}

// Run executes one HTTP request and returns the result.
// It does not bail on non-2xx — the status code is recorded and returned.
func (r *HTTPRunner) Run(ctx context.Context, req Request) StepResult {
	if r == nil || r.client == nil {
		return StepResult{Method: req.Method, URL: req.URL, Err: nil}
	}

	var body io.Reader
	if req.Body != "" {
		body = bytes.NewBufferString(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return StepResult{Method: req.Method, URL: req.URL, Err: err}
	}

	for k, v := range req.Headers {
		if k == "" {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := r.client.Do(httpReq)
	dur := time.Since(start)
	if err != nil {
		return StepResult{Method: req.Method, URL: req.URL, Duration: dur, Err: err}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	return StepResult{
		Method:     req.Method,
		URL:        req.URL,
		StatusCode: resp.StatusCode,
		Duration:   dur,
		Body:       respBody,
	}
}

// VirtualUser is one concurrent user running a scenario repeatedly.
// Each user has its own Vars map — no sharing between users.
type VirtualUser struct {
	ID   int
	Vars map[string]any
}

// ScenarioResult is one full scenario iteration by one virtual user.
type ScenarioResult struct {
	UserID       int
	ScenarioName string
	Start        time.Time
	Duration     time.Duration
	Steps        []StepResult
}

// RunScenarioOnceForTest is an exported wrapper for runScenarioOnce used in tests.
func RunScenarioOnceForTest(ctx context.Context, s Scenario, runner *HTTPRunner) []StepResult {
	return runScenarioOnce(ctx, s, runner)
}

// varPattern matches {{varname}} in URLs, headers, and bodies.
var varPattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// interpolate replaces {{key}} in s with vars[key]. If key not found, leaves {{key}} as-is.
func interpolate(s string, vars map[string]any) string {
	if s == "" {
		return s
	}
	if vars == nil {
		return s
	}
	if !strings.Contains(s, "{{") {
		return s
	}
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := varPattern.FindStringSubmatch(match)
		if len(sub) != 2 {
			return match
		}
		key := sub[1]
		if key == "" {
			return match
		}
		if v, ok := vars[key]; ok {
			return fmt.Sprint(v)
		}
		return match
	})
}

// InterpolateForTest is an exported wrapper for interpolate used in tests.
func InterpolateForTest(s string, vars map[string]any) string {
	return interpolate(s, vars)
}

// RunScenarioOnceWithUserForTest is an exported wrapper for runScenarioOnceWithUser used in tests.
func RunScenarioOnceWithUserForTest(ctx context.Context, s Scenario, runner *HTTPRunner, user *VirtualUser) []StepResult {
	return runScenarioOnceWithUser(ctx, s, runner, user)
}

// extractValue pulls a value from a JSON body using a path like "$.token".
// It returns the value and whether it was found. Simple, no reflection.
func extractValue(body []byte, path string) (any, bool) {
	if len(body) == 0 {
		return nil, false
	}
	if path == "" {
		return nil, false
	}
	p := strings.TrimSpace(path)
	if p == "$" {
		return nil, false
	}
	if strings.HasPrefix(p, "$.") {
		p = strings.TrimPrefix(p, "$.")
	} else if strings.HasPrefix(p, "$") {
		p = strings.TrimPrefix(p, "$")
		p = strings.TrimPrefix(p, ".")
	}
	if p == "" {
		return nil, false
	}
	result := gjson.GetBytes(body, p)
	if !result.Exists() {
		return nil, false
	}
	return result.Value(), true
}

// runScenarioOnceWithUser runs steps with variable extraction into user.Vars.
// After each step, each key in step.Extract is extracted from the response body
// and stored in user.Vars. If extraction fails, the var is left unset.
func runScenarioOnceWithUser(ctx context.Context, s Scenario, runner *HTTPRunner, user *VirtualUser) []StepResult {
	if runner == nil {
		return nil
	}
	if ctx == nil {
		return nil
	}
	if user == nil {
		return runScenarioOnce(ctx, s, runner)
	}
	if user.Vars == nil {
		user.Vars = make(map[string]any)
	}

	results := make([]StepResult, 0, len(s.Steps))

	for _, step := range s.Steps {
		// interpolate URL, body, and headers with current vars
		url := interpolate(step.URL, user.Vars)
		body := interpolate(step.Body, user.Vars)

		var headers map[string]string
		if step.Headers != nil {
			headers = make(map[string]string, len(step.Headers))
			for k, v := range step.Headers {
				headers[k] = interpolate(v, user.Vars)
			}
		}

		req := Request{
			Method:  step.Method,
			URL:     url,
			Body:    body,
			Headers: headers,
		}

		result := runner.Run(ctx, req)
		results = append(results, result)

		if step.Extract != nil && result.Err == nil {
			for key, path := range step.Extract {
				if key == "" {
					continue
				}
				if path == "" {
					continue
				}
				val, ok := extractValue(result.Body, path)
				if ok {
					user.Vars[key] = val
				}
			}
		}

		if ctx.Err() != nil {
			break
		}
	}

	return results
}

// pickScenario picks one scenario weighted at random, per VU launch.
// It uses simple weighted random, not per-iteration.
func pickScenario(scenarios []Scenario) Scenario {
	if len(scenarios) == 0 {
		return Scenario{}
	}
	if len(scenarios) == 1 {
		return scenarios[0]
	}
	total := 0
	for _, s := range scenarios {
		w := s.Weight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	if total <= 0 {
		return scenarios[0]
	}
	r := rand.Intn(total)
	for _, s := range scenarios {
		w := s.Weight
		if w <= 0 {
			w = 1
		}
		r -= w
		if r < 0 {
			return s
		}
	}
	return scenarios[len(scenarios)-1]
}

// PickScenarioForTest is an exported wrapper for pickScenario used in tests.
func PickScenarioForTest(scenarios []Scenario) Scenario {
	return pickScenario(scenarios)
}

// RunUserForTest is an exported wrapper for runUser used in tests.
func RunUserForTest(ctx context.Context, id int, s Scenario, runner *HTTPRunner, results chan<- ScenarioResult) {
	runUser(ctx, id, s, runner, results)
}

// runUser keeps running the scenario repeatedly until context expires.
// Results are sent on the channel — no shared slice, no mutex needed.
func runUser(ctx context.Context, id int, s Scenario, runner *HTTPRunner, results chan<- ScenarioResult) {
	if ctx == nil {
		return
	}
	if runner == nil {
		return
	}
	if results == nil {
		return
	}

	user := VirtualUser{
		ID:   id,
		Vars: make(map[string]any),
	}

	for ctx.Err() == nil {
		start := time.Now()
		stepResults := runScenarioOnceWithUser(ctx, s, runner, &user)
		results <- ScenarioResult{
			UserID:       id,
			ScenarioName: s.Name,
			Start:        start,
			Duration:     time.Since(start),
			Steps:        stepResults,
		}
	}
}

// runUserWithScenarios picks one scenario per VU (weighted) and runs it repeatedly.
func runUserWithScenarios(ctx context.Context, id int, scenarios []Scenario, runner *HTTPRunner, results chan<- ScenarioResult) {
	if ctx == nil {
		return
	}
	if runner == nil {
		return
	}
	if results == nil {
		return
	}
	if len(scenarios) == 0 {
		return
	}

	picked := pickScenario(scenarios)
	if picked.Name == "" {
		picked.Name = "scenario"
	}

	user := VirtualUser{
		ID:   id,
		Vars: make(map[string]any),
	}

	for ctx.Err() == nil {
		start := time.Now()
		stepResults := runScenarioOnceWithUser(ctx, picked, runner, &user)
		results <- ScenarioResult{
			UserID:       id,
			ScenarioName: picked.Name,
			Start:        start,
			Duration:     time.Since(start),
			Steps:        stepResults,
		}
	}
}

// RunScenario runs a scenario repeatedly with concurrency virtual users until duration expires.
// It is simple and readable: context timeout, VU goroutines, channel, wg, collect.
func RunScenario(ctx context.Context, s Scenario, cfg OrchestratorConfig, runner *HTTPRunner, stats *RunStats) []ScenarioResult {
	if runner == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	duration := time.Duration(cfg.Duration)
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}
	if duration <= 0 {
		duration = time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	results := make(chan ScenarioResult, concurrency*10)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runUser(ctx, id, s, runner, results)
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var all []ScenarioResult
	for r := range results {
		if stats != nil {
			stats.ScenLoops.Add(1)
			for _, st := range r.Steps {
				if st.Err != nil || st.StatusCode >= 400 {
					stats.ScenErr.Add(1)
				}
			}
		}
		all = append(all, r)
	}

	return all
}

// RunScenarios runs multiple weighted scenarios. Each VU picks one scenario once at launch.
func RunScenarios(ctx context.Context, scenarios []Scenario, cfg OrchestratorConfig, runner *HTTPRunner, stats *RunStats) []ScenarioResult {
	if runner == nil {
		return nil
	}
	if len(scenarios) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	duration := time.Duration(cfg.Duration)
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}
	if duration <= 0 {
		duration = time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	results := make(chan ScenarioResult, concurrency*10)

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runUserWithScenarios(ctx, id, scenarios, runner, results)
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var all []ScenarioResult
	for r := range results {
		if stats != nil {
			stats.ScenLoops.Add(1)
			for _, st := range r.Steps {
				if st.Err != nil || st.StatusCode >= 400 {
					stats.ScenErr.Add(1)
				}
			}
		}
		all = append(all, r)
	}

	return all
}

// runScenarioOnce runs all steps in a scenario once, in order.
// It records every step result even if a step fails (non-2xx or network error).
// It stops cleanly if the context expires mid-scenario.
func runScenarioOnce(ctx context.Context, s Scenario, runner *HTTPRunner) []StepResult {
	if runner == nil {
		return nil
	}
	if ctx == nil {
		return nil
	}

	results := make([]StepResult, 0, len(s.Steps))

	for _, step := range s.Steps {
		req := Request{
			Method:  step.Method,
			URL:     step.URL,
			Body:    step.Body,
			Headers: step.Headers,
		}

		result := runner.Run(ctx, req)
		results = append(results, result)

		if ctx.Err() != nil {
			break
		}
	}

	return results
}
