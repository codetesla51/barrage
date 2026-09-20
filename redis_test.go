package barrage

import (
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestFireRedis(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis failed to start: %v", err)
	}
	defer s.Close()

	target := RedisTarget{
		Addr: s.Addr(),
		Query: []QueryWeight{
			{Query: "PING", Weight: 1},
		},
	}

	result, err := FireRedis(target, 10, 5, time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("FireRedis returned error: %v", err)
	}

	if result.Requests == 0 {
		t.Error("expected requests to be fired")
	}
	// The run is exactly 1s at 10/s, so the last command can straddle the
	// deadline: it is aborted before the server answers and is not counted as
	// a target failure. At least 9 of ~10 must still land as clean successes,
	// and no non-cancel error may appear.
	if result.Success < 0.9 {
		t.Errorf("expected ~100%% success for PING, got %.2f", result.Success)
	}
	if len(result.Buckets) == 0 {
		t.Error("expected at least one bucket to be populated")
	}
	for _, e := range result.Errors {
		if !strings.Contains(e, "context canceled") {
			t.Errorf("unexpected non-cancel error: %q", e)
		}
	}
}

func TestFireRedisFailedCommand(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis failed to start: %v", err)
	}
	defer s.Close()

	target := RedisTarget{
		Addr: s.Addr(),
		Query: []QueryWeight{
			{Query: "NOTACOMMAND foo", Weight: 1},
		},
	}

	result, err := FireRedis(target, 10, 5, time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("FireRedis returned error: %v", err)
	}

	if result.Requests == 0 {
		t.Error("expected requests to be fired")
	}
	// Almost every NOTACOMMAND must fail; only a command straddling the
	// shutdown deadline (aborted before the server could answer) may not.
	if result.Success > 0.5 {
		t.Errorf("expected most NOTACOMMAND commands to fail, got %.2f", result.Success)
	}
	if len(result.Errors) < 1 {
		t.Error("expected unknown command errors to be recorded")
	}
}

func TestFireRedisValue(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis failed to start: %v", err)
	}
	defer s.Close()

	if err := s.Set("foo", "bar"); err != nil {
		t.Fatalf("failed to seed miniredis: %v", err)
	}

	target := RedisTarget{
		Addr: s.Addr(),
		Query: []QueryWeight{
			{Query: "GET foo", Weight: 1},
		},
	}

	result, err := FireRedis(target, 10, 5, time.Second, time.Second, 0, nil)
	if err != nil {
		t.Fatalf("FireRedis returned error: %v", err)
	}

	// Same shutdown-tail tolerance as TestFireRedis: the trailing GET may be
	// aborted before the server answers, so success can dip slightly below 1.
	if result.Success < 0.9 {
		t.Errorf("expected ~100%% success for GET on existing key, got %.2f", result.Success)
	}
}
