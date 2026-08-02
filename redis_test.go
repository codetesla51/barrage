package barrage

import (
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

	result, err := FireRedis(target, 10, 5, time.Second, time.Second, 0)
	if err != nil {
		t.Fatalf("FireRedis returned error: %v", err)
	}

	if result.Requests == 0 {
		t.Error("expected requests to be fired")
	}
	if result.Success != 1.0 {
		t.Errorf("expected 100%% success for PING, got %.2f", result.Success)
	}
	if len(result.Buckets) == 0 {
		t.Error("expected at least one bucket to be populated")
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
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

	result, err := FireRedis(target, 10, 5, time.Second, time.Second, 0)
	if err != nil {
		t.Fatalf("FireRedis returned error: %v", err)
	}

	if result.Requests == 0 {
		t.Error("expected requests to be fired")
	}
	if result.Success != 0 {
		t.Errorf("expected 0%% success for unknown command, got %.2f", result.Success)
	}
	if len(result.Errors) == 0 {
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

	result, err := FireRedis(target, 10, 5, time.Second, time.Second, 0)
	if err != nil {
		t.Fatalf("FireRedis returned error: %v", err)
	}

	if result.Success != 1.0 {
		t.Errorf("expected 100%% success for GET on existing key, got %.2f", result.Success)
	}
}
