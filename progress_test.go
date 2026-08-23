package barrage

import (
	"testing"
	"time"
)

func TestRunProgressRecordAndSnapshot(t *testing.T) {
	p := NewRunProgress(30*time.Second, 0, 5)
	p.Record("http", true, 10*time.Millisecond)
	p.Record("http", true, 30*time.Millisecond)
	p.Record("http", false, 100*time.Millisecond)

	snap := p.Snapshot()
	if len(snap.Runners) != 1 {
		t.Fatalf("expected 1 runner, got %d", len(snap.Runners))
	}
	r := snap.Runners[0]
	if r.Name != "http" || r.Requests != 3 {
		t.Fatalf("unexpected runner row: %+v", r)
	}
	if r.SuccessPct < 66.6 || r.SuccessPct > 66.7 {
		t.Fatalf("success pct = %.2f, want ~66.7", r.SuccessPct)
	}
	if r.P99 != 100*time.Millisecond {
		t.Fatalf("p99 = %s, want 100ms", r.P99)
	}
	if r.P50 != 10*time.Millisecond && r.P50 != 30*time.Millisecond {
		t.Fatalf("p50 = %s, want 10ms or 30ms", r.P50)
	}
}

func TestRunProgressState(t *testing.T) {
	// no ramp: with hits recorded the state is running
	p := NewRunProgress(10*time.Second, 0, 5)
	p.Record("db", true, time.Millisecond)
	if got := p.Snapshot().State; got != "running" {
		t.Fatalf("state = %q, want running", got)
	}

	// ramping: fresh run inside ramp window with no hits yet
	p2 := NewRunProgress(10*time.Second, 5*time.Second, 5)
	if got := p2.Snapshot().State; got != "starting" {
		t.Fatalf("state = %q, want starting", got)
	}
}

func TestRunProgressConcurrentRecords(t *testing.T) {
	p := NewRunProgress(time.Second, 0, 8)
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			for j := 0; j < 500; j++ {
				p.Record("redis", true, time.Millisecond)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	snap := p.Snapshot()
	if snap.Runners[0].Requests != 2000 {
		t.Fatalf("requests = %d, want 2000 (lost updates?)", snap.Runners[0].Requests)
	}
}

func TestOrchestratorWithProgressMatchesOrchestratorShape(t *testing.T) {
	// a tiny http-only config against an unreachable port still records
	// failures into progress — proving the hook is wired end to end.
	cfg := OrchestratorConfig{
		Duration:    Duration(300 * time.Millisecond),
		BucketWidth: Duration(time.Second),
		Concurrency: 2,
		HTTP: &HTTPRunnerConfig{
			Rate: 20,
			Target: HTTPTarget{
				Method: "GET",
				URL:    "http://127.0.0.1:1/nope",
			},
		},
	}
	prog := NewRunProgress(time.Duration(cfg.Duration), 0, 2)
	if _, err := OrchestratorWithProgress(cfg, prog); err != nil {
		t.Fatalf("orchestrator errored: %v", err)
	}
	snap := prog.Snapshot()
	if len(snap.Runners) == 0 || snap.Runners[0].Requests == 0 {
		t.Fatalf("progress recorded nothing: %+v", snap)
	}
}
