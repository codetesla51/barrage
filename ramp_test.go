package barrage

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPlanCoarseLevels(t *testing.T) {
	cases := []struct {
		name       string
		start, max int
		want       []int
	}{
		{"doubling to max", 10, 160, []int{10, 20, 40, 80, 160}},
		{"max off grid included", 10, 100, []int{10, 20, 40, 80, 100}},
		{"single level", 10, 10, []int{10}},
		{"max below start", 20, 10, []int{20}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planCoarseLevels(c.start, c.max)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestPlanFineLevels(t *testing.T) {
	cases := []struct {
		name       string
		ok, broken int
		want       []int
	}{
		{"wide gap quarters", 80, 160, []int{100, 120, 140}},
		{"adjacent none", 80, 81, nil},
		{"same none", 80, 80, nil},
		{"small gap ones", 80, 83, []int{81, 82}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planFineLevels(c.ok, c.broken)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
			for _, v := range got {
				if v <= c.ok || v >= c.broken {
					t.Fatalf("level %d outside (%d, %d)", v, c.ok, c.broken)
				}
			}
		})
	}
}

func TestScaledRate(t *testing.T) {
	cases := []struct {
		name             string
		base, conc, step int
		want             int
	}{
		{"double conc doubles rate", 50, 10, 20, 100},
		{"quad", 50, 10, 40, 200},
		{"floor at one", 1, 10, 1, 1},
		{"zero base passthrough", 0, 10, 20, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scaledRate(c.base, c.conc, c.step); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestRampBreakers(t *testing.T) {
	th := 100 * time.Millisecond
	cases := []struct {
		name             string
		httpP99, dbP99   time.Duration
		httpSucc, dbSucc float64
		httpOK, dbOK     bool
		wantBroken       bool
		wantBy           []string
	}{
		{"all under holds", 50 * time.Millisecond, 50 * time.Millisecond, 1.0, 1.0, true, true, false, nil},
		{"db over attributes db", 10 * time.Millisecond, 200 * time.Millisecond, 1.0, 1.0, true, true, true, []string{"db"}},
		{"both over attributes both", 200 * time.Millisecond, 200 * time.Millisecond, 1.0, 1.0, true, true, true, []string{"http", "db"}},
		{"errors attribute the failing runner", 10 * time.Millisecond, 10 * time.Millisecond, 1.0, 0.5, true, true, true, []string{"db"}},
		{"at threshold holds", 100 * time.Millisecond, 100 * time.Millisecond, 1.0, 1.0, true, true, false, nil},
		{"runner that did not run ignored", 500 * time.Millisecond, 0, 1.0, 0, false, false, false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rampBreakers(c.httpP99, c.dbP99, 0, 0, c.httpSucc, c.dbSucc, 1.0, 1.0, c.httpOK, c.dbOK, false, false, th, th, th)
			if (len(got) > 0) != c.wantBroken {
				t.Fatalf("broken = %v, want %v (by=%v)", len(got) > 0, c.wantBroken, got)
			}
			if len(got) != len(c.wantBy) {
				t.Fatalf("got %v, want %v", got, c.wantBy)
			}
			for i := range got {
				if got[i] != c.wantBy[i] {
					t.Fatalf("got %v, want %v", got, c.wantBy)
				}
			}
		})
	}
}

func TestRunAutoRamp_HTTPFullSweep(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := OrchestratorConfig{
		Duration:    Duration(15 * time.Second),
		BucketWidth: Duration(time.Second),
		Concurrency: 2,
		HTTP: &HTTPRunnerConfig{
			Target: HTTPTarget{Method: "GET", URL: ts.URL},
			Rate:   20,
		},
	}
	ramp := AutoRampConfig{MaxConcurrency: 8, StepDuration: Duration(1200 * time.Millisecond)}
	// Generous threshold so nothing breaks and the full coarse grid runs.
	res, err := RunAutoRamp(cfg, ramp, 5*time.Second, 5*time.Second, 5*time.Second)
	if err != nil {
		t.Fatalf("RunAutoRamp: %v", err)
	}
	if len(res.Steps) == 0 {
		t.Fatal("expected at least one step")
	}
	if res.Steps[0].Concurrency != 2 {
		t.Errorf("first step = %d, want 2", res.Steps[0].Concurrency)
	}
	seen := map[int]bool{}
	for _, s := range res.Steps {
		if seen[s.Concurrency] {
			t.Errorf("duplicate level %d", s.Concurrency)
		}
		seen[s.Concurrency] = true
		if s.Requests == 0 {
			t.Errorf("level %d fired nothing", s.Concurrency)
		}
		if s.Broken {
			t.Errorf("level %d unexpectedly broken (p99 %s)", s.Concurrency, s.P99)
		}
	}
	for _, want := range []int{2, 4, 8} {
		if !seen[want] {
			t.Errorf("expected coarse level %d to run, ran %v", want, seen)
		}
	}
	if res.BreakAt != 0 {
		t.Errorf("BreakAt = %d, want 0 (nothing broke)", res.BreakAt)
	}
	if res.LastOK != 8 {
		t.Errorf("LastOK = %d, want 8", res.LastOK)
	}
}

func TestRunAutoRamp_HTTPImmediateBreak(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := OrchestratorConfig{
		Duration:    Duration(15 * time.Second),
		BucketWidth: Duration(time.Second),
		Concurrency: 2,
		HTTP: &HTTPRunnerConfig{
			Target: HTTPTarget{Method: "GET", URL: ts.URL},
			Rate:   20,
		},
	}
	ramp := AutoRampConfig{MaxConcurrency: 8, StepDuration: Duration(1200 * time.Millisecond)}
	// Impossible threshold breaks the first level; fine fill probes below it.
	res, err := RunAutoRamp(cfg, ramp, time.Nanosecond, 100*time.Millisecond, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("RunAutoRamp: %v", err)
	}
	if res.BreakAt == 0 {
		t.Error("expected a break with a 1ns threshold")
	}
}

func TestRunAutoRamp_RejectsBadMax(t *testing.T) {
	cfg := OrchestratorConfig{
		Duration:    Duration(5 * time.Second),
		BucketWidth: Duration(time.Second),
		Concurrency: 10,
		HTTP: &HTTPRunnerConfig{
			Target: HTTPTarget{Method: "GET", URL: "http://localhost:1"},
			Rate:   1,
		},
	}
	_, err := RunAutoRamp(cfg, AutoRampConfig{MaxConcurrency: 5}, 100*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond)
	if err == nil {
		t.Error("expected error for max below start")
	}
}

func TestLoadConfig_AutoRamp(t *testing.T) {
	cfg, err := LoadConfigBytes([]byte("duration: 15s\nconcurrency: 10\nhttp:\n  rate: 5\n  target:\n    method: GET\n    url: http://localhost:8080/\nauto_ramp:\n  max_concurrency: 80\n  step_duration: 10s\n"))
	if err != nil {
		t.Fatalf("LoadConfigBytes: %v", err)
	}
	if cfg.AutoRamp == nil || cfg.AutoRamp.MaxConcurrency != 80 {
		t.Fatalf("auto_ramp not parsed: %+v", cfg.AutoRamp)
	}
	bad, err := LoadConfigBytes([]byte("duration: 15s\nconcurrency: 10\nhttp:\n  rate: 5\n  target:\n    method: GET\n    url: http://localhost:8080/\nauto_ramp:\n  max_concurrency: 5\n  step_duration: 10s\n"))
	if err == nil || bad != nil {
		t.Errorf("expected max<start to fail, got %v, %v", bad, err)
	}
}
