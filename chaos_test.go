package barrage

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/codetesla51/barrage/internal/chaos"
)

// requireChaosManager skips when Toxiproxy is not running locally.
func requireChaosManager(t *testing.T) *chaos.Manager {
	t.Helper()
	m, err := chaos.NewManager(chaos.DefaultAPIAddr)
	if err != nil {
		t.Skipf("toxiproxy not reachable: %v", err)
	}
	return m
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free addr: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// fakeController records Add/Remove calls without touching the network.
type fakeController struct {
	mu      sync.Mutex
	adds    []string
	removes []string
}

func (f *fakeController) AddToxicFull(proxyName, toxicName, toxicType, stream string, toxicity float32, attrs map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds = append(f.adds, proxyName+"/"+toxicName)
	return nil
}

func (f *fakeController) RemoveToxic(proxyName, toxicName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, proxyName+"/"+toxicName)
	return nil
}

func (f *fakeController) addCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.adds)
}

func TestEffectiveTargets(t *testing.T) {
	db := DBTarget{Conn: "postgres://real:5432/db", ChaosConn: "postgres://proxy:26000/db"}
	if got := effectiveDBConn(db, true); got != "postgres://proxy:26000/db" {
		t.Errorf("chaos db conn = %q", got)
	}
	if got := effectiveDBConn(db, false); got != "postgres://real:5432/db" {
		t.Errorf("normal db conn = %q", got)
	}
	db2 := DBTarget{Conn: "postgres://real:5432/db"}
	if got := effectiveDBConn(db2, true); got != "postgres://real:5432/db" {
		t.Errorf("unset chaos db conn should fall back, got %q", got)
	}

	redis := RedisTarget{Addr: "localhost:6379", ChaosAddr: "localhost:26001"}
	if got := effectiveRedisAddr(redis, true); got != "localhost:26001" {
		t.Errorf("chaos redis addr = %q", got)
	}
	if got := effectiveRedisAddr(redis, false); got != "localhost:6379" {
		t.Errorf("normal redis addr = %q", got)
	}

	h := HTTPTarget{URL: "http://real:8080/api", ChaosURL: "http://127.0.0.1:26002/api"}
	if got := effectiveHTTPURL(h, true); got != "http://127.0.0.1:26002/api" {
		t.Errorf("chaos http url = %q", got)
	}
	if got := effectiveHTTPURL(h, false); got != "http://real:8080/api" {
		t.Errorf("normal http url = %q", got)
	}

	step := Step{URL: "http://real/x", ChaosURL: "http://proxy/x"}
	if got := effectiveStepURL(step, true); got != "http://proxy/x" {
		t.Errorf("chaos step url = %q", got)
	}
	if got := effectiveStepURL(step, false); got != "http://real/x" {
		t.Errorf("normal step url = %q", got)
	}
}

func TestValidateChaos(t *testing.T) {
	good := &ChaosConfig{
		Proxies: []ChaosProxyConfig{{Name: "db-proxy", Listen: "127.0.0.1:26000", Upstream: "127.0.0.1:5432"}},
		Faults: []ChaosFaultConfig{{
			At: Duration(2 * time.Second), Duration: Duration(2 * time.Second),
			Proxy: "db-proxy", Type: "latency", Attrs: map[string]any{"latency": 500},
		}},
	}
	if err := validateChaos(good, 10*time.Second); err != nil {
		t.Errorf("good chaos rejected: %v", err)
	}
	cases := []struct {
		name string
		cfg  *ChaosConfig
	}{
		{"empty proxy name", &ChaosConfig{Proxies: []ChaosProxyConfig{{Listen: "a", Upstream: "b"}}}},
		{"unknown proxy in fault", &ChaosConfig{
			Proxies: []ChaosProxyConfig{{Name: "p", Listen: "a", Upstream: "b"}},
			Faults:  []ChaosFaultConfig{{Proxy: "missing", Type: "latency", Attrs: map[string]any{"latency": 100}}},
		}},
		{"bad toxic type", &ChaosConfig{Faults: []ChaosFaultConfig{{Proxy: "p", Type: "nuke"}}}},
		// packet_loss is documented upstream but absent from the pinned
		// server release — validation must reject it (proven by a CI run
		// that logged add_failed for it).
		{"packet_loss unsupported", &ChaosConfig{Faults: []ChaosFaultConfig{{Proxy: "p", Type: "packet_loss"}}}},
		{"bad stream", &ChaosConfig{Faults: []ChaosFaultConfig{{Proxy: "p", Type: "latency", Stream: "sideways"}}}},
		{"at past duration", &ChaosConfig{Faults: []ChaosFaultConfig{{Proxy: "p", Type: "latency", At: Duration(20 * time.Second)}}}},
	}
	for _, c := range cases {
		if err := validateChaos(c.cfg, 10*time.Second); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestSchedulerFakeTiming(t *testing.T) {
	fake := &fakeController{}
	events := []FaultEvent{
		{At: 100 * time.Millisecond, Duration: 150 * time.Millisecond, Proxy: "p", ToxicType: "latency", Attrs: map[string]any{"latency": 100}},
		{At: 200 * time.Millisecond, Duration: 100 * time.Millisecond, Proxy: "p", ToxicType: "bandwidth", Attrs: map[string]any{"rate": 10}},
	}
	sched := NewScheduler(fake, events, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	log := sched.Run(ctx)
	if got := fake.addCount(); got != 2 {
		t.Fatalf("adds = %d, want 2", got)
	}
	// Each timed fault logs add + remove.
	adds, removes := 0, 0
	for _, e := range log {
		switch e.Action {
		case "add":
			adds++
		case "remove":
			removes++
		}
		if e.Offset < 0 || e.Offset > 700*time.Millisecond {
			t.Errorf("event offset %v out of range", e.Offset)
		}
	}
	if adds != 2 || removes != 2 {
		t.Errorf("adds=%d removes=%d, want 2/2", adds, removes)
	}
}

func TestSchedulerRealToxics(t *testing.T) {
	m := requireChaosManager(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	upstream := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	listen := freeTCPAddr(t)
	const proxy = "barrage-test-sched"
	if err := m.CreateProxy(proxy, listen, upstream); err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	t.Cleanup(func() { _ = m.TeardownProxy(proxy) })

	events := []FaultEvent{
		{At: 100 * time.Millisecond, Duration: 400 * time.Millisecond, Proxy: proxy, ToxicType: "latency", Attrs: map[string]any{"latency": 200}},
	}
	sched := NewScheduler(m, events, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	done := make(chan []ChaosEvent, 1)
	go func() { done <- sched.Run(ctx) }()

	// During the window the toxic must be present.
	time.Sleep(250 * time.Millisecond)
	has, err := m.HasToxic(proxy, "latency_downstream")
	if err != nil {
		t.Fatalf("has toxic: %v", err)
	}
	if !has {
		t.Fatal("toxic missing inside its window")
	}
	log := <-done
	// After the window it must be gone.
	has, err = m.HasToxic(proxy, "latency_downstream")
	if err != nil {
		t.Fatalf("has toxic: %v", err)
	}
	if has {
		t.Fatal("toxic still present after its window")
	}
	if len(log) < 2 {
		t.Fatalf("event log = %v, want add+remove", log)
	}
}

func TestRedisThroughIdleProxy(t *testing.T) {
	m := requireChaosManager(t)
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer s.Close()
	listen := freeTCPAddr(t)
	const proxy = "barrage-test-redis"
	if err := m.CreateProxy(proxy, listen, s.Addr()); err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	t.Cleanup(func() { _ = m.TeardownProxy(proxy) })

	// Idle proxy must be transparent: same success as direct.
	direct, err := FireRedis(RedisTarget{Addr: s.Addr(), Query: []QueryWeight{{Query: "PING", Weight: 1}}}, 10, 5, time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("direct redis: %v", err)
	}
	via, err := FireRedis(RedisTarget{Addr: listen, Query: []QueryWeight{{Query: "PING", Weight: 1}}}, 10, 5, time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("proxy redis: %v", err)
	}
	if direct.Success < 0.9 || via.Success < 0.9 {
		t.Fatalf("direct success=%.2f proxy success=%.2f, want >=0.9", direct.Success, via.Success)
	}

	// With a latency toxic the runner metrics must show the delay.
	if err := m.AddToxic(proxy, "latency_downstream", "latency", map[string]any{"latency": 300}); err != nil {
		t.Fatalf("add toxic: %v", err)
	}
	defer func() { _ = m.RemoveToxic(proxy, "latency_downstream") }()
	slow, err := FireRedis(RedisTarget{Addr: listen, Query: []QueryWeight{{Query: "PING", Weight: 1}}}, 5, 2, 2*time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("slow redis: %v", err)
	}
	if slow.P50 < 200*time.Millisecond {
		t.Errorf("toxic p50 = %v, want >=200ms (300ms toxic)", slow.P50)
	}
}

func TestHTTPThroughIdleProxy(t *testing.T) {
	m := requireChaosManager(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	upstream := strings.TrimPrefix(ts.URL, "http://")
	listen := freeTCPAddr(t)
	const proxy = "barrage-test-http"
	if err := m.CreateProxy(proxy, listen, upstream); err != nil {
		t.Fatalf("create proxy: %v", err)
	}
	t.Cleanup(func() { _ = m.TeardownProxy(proxy) })

	proxyURL := "http://" + listen
	direct, err := FireHTTP(HTTPTarget{URL: ts.URL}, 10, 2, time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("direct http: %v", err)
	}
	via, err := FireHTTP(HTTPTarget{URL: proxyURL}, 10, 2, time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("proxy http: %v", err)
	}
	if direct.Requests == 0 || via.Requests == 0 {
		t.Fatalf("direct=%d proxy=%d, want requests", direct.Requests, via.Requests)
	}
}

func TestOrchestratorChaosEndToEnd(t *testing.T) {
	// Works against a running server or via auto-spawn; skips only when
	// neither is available (no server reachable and no binary in PATH).
	if _, err := chaos.NewManager(chaos.DefaultAPIAddr); err != nil {
		if _, lerr := exec.LookPath("toxiproxy-server"); lerr != nil {
			t.Skipf("toxiproxy not reachable and no binary to spawn: %v", err)
		}
	}
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer s.Close()
	listen := freeTCPAddr(t)

	cfg := OrchestratorConfig{
		Duration:    Duration(3 * time.Second),
		BucketWidth: Duration(time.Second),
		Concurrency: 2,
		Redis: &RedisRunnerConfig{
			Target: RedisTarget{
				Addr:      s.Addr(),
				ChaosAddr: listen,
				Query:     []QueryWeight{{Query: "PING", Weight: 1}},
			},
			Rate: 5,
		},
		Chaos: &ChaosConfig{
			Proxies: []ChaosProxyConfig{{Name: "barrage-e2e-redis", Listen: listen, Upstream: s.Addr()}},
			Faults: []ChaosFaultConfig{{
				At: Duration(time.Second), Duration: Duration(time.Second),
				Proxy: "barrage-e2e-redis", Type: "latency", Attrs: map[string]any{"latency": 300},
			}},
		},
		Quiet: true,
	}
	res, err := Orchestrator(cfg)
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}
	t.Cleanup(func() {
		m2, err := chaos.NewManager(chaos.DefaultAPIAddr)
		if err == nil {
			_ = m2.TeardownProxy("barrage-e2e-redis")
		}
	})
	if len(res.ChaosEvents) < 2 {
		t.Fatalf("chaos events = %v, want add+remove", res.ChaosEvents)
	}
	// Event offsets must align with the schedule (1s ± tolerance).
	foundAdd := false
	for _, e := range res.ChaosEvents {
		if e.Action == "add" && e.Proxy == "barrage-e2e-redis" {
			foundAdd = true
			if e.Offset < 500*time.Millisecond || e.Offset > 1500*time.Millisecond {
				t.Errorf("add offset = %v, want ~1s", e.Offset)
			}
		}
	}
	if !foundAdd {
		t.Error("no add event for the scheduled fault")
	}
	// Correlation input: events must survive into the report and JSON.
	corr := Correlate(res, 100*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond)
	data := NewReportData(res, corr)
	data.Duration = "3s"
	if len(data.ChaosEvents) != len(res.ChaosEvents) {
		t.Errorf("report chaos events = %d, want %d", len(data.ChaosEvents), len(res.ChaosEvents))
	}
	buf, err := BuildJSON(data)
	if err != nil {
		t.Fatalf("build json: %v", err)
	}
	if !strings.Contains(string(buf), "chaos_events") {
		t.Error("JSON missing chaos_events")
	}
	if !strings.Contains(string(buf), "barrage-e2e-redis") {
		t.Error("JSON missing proxy name")
	}
}

func TestOrchestratorNoChaosUnchanged(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	cfg := OrchestratorConfig{
		Duration:    Duration(time.Second),
		BucketWidth: Duration(time.Second),
		HTTP:        &HTTPRunnerConfig{Target: HTTPTarget{URL: ts.URL}, Rate: 10},
		Quiet:       true,
	}
	res, err := Orchestrator(cfg)
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}
	if len(res.ChaosEvents) != 0 {
		t.Errorf("normal run chaos events = %v, want none", res.ChaosEvents)
	}
}

func TestLoadConfigChaosExamples(t *testing.T) {
	for _, path := range []string{"examples/chaos-redis.yaml", "examples/chaos-full.yaml", "examples/chaos-break-all.yaml"} {
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Errorf("example %s failed to load: %v", path, err)
			continue
		}
		if cfg.Chaos == nil || len(cfg.Chaos.Proxies) == 0 || len(cfg.Chaos.Faults) == 0 {
			t.Errorf("example %s: want proxies and faults, got %+v", path, cfg.Chaos)
		}
	}
}

func TestLoadConfigChaos(t *testing.T) {
	path := writeConfig(t, `
duration: 5s
bucket_width: 1s
redis:
  rate: 5
  target:
    addr: localhost:6379
    chaos_addr: localhost:26001
    queries:
      - query: PING
        weight: 1
chaos:
  api: localhost:8474
  proxies:
    - name: redis-proxy
      listen: localhost:26001
      upstream: localhost:6379
  faults:
    - at: 1s
      duration: 1s
      proxy: redis-proxy
      type: latency
      attrs:
        latency: 300
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load chaos config: %v", err)
	}
	if cfg.Chaos == nil || len(cfg.Chaos.Proxies) != 1 || len(cfg.Chaos.Faults) != 1 {
		t.Fatalf("chaos config = %+v", cfg.Chaos)
	}
	if cfg.Redis.Target.ChaosAddr != "localhost:26001" {
		t.Errorf("chaos_addr = %q", cfg.Redis.Target.ChaosAddr)
	}
}
