package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// testRedis spins up an in-process redis and wires it into rc for the test.
func testRedis(t *testing.T) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		client.Close()
		rc = nil
	})
	rc = client
}

// TestReadCachesInRedis verifies the miss -> DB -> set -> serve flow.
func TestReadCachesInRedis(t *testing.T) {
	testRedis(t)

	req := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	rec := httptest.NewRecorder()
	handleProducts(rec, req)
	first := rec.Body.String()
	if !strings.Contains(first, "widget") {
		t.Fatalf("first (miss) response lacks products: %q", first)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	rec2 := httptest.NewRecorder()
	handleProducts(rec2, req2)
	if rec2.Body.String() != first {
		t.Fatal("second (hit) response differs from cached first")
	}
}

// TestOrderCreateInvalidatesCache proves no stale orders survive a write.
func TestOrderCreateInvalidatesCache(t *testing.T) {
	testRedis(t)

	rec := httptest.NewRecorder()
	handleOrdersList(rec)
	if !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("orders stub response wrong: %q", rec.Body.String())
	}
	if got := rc.Get(context.Background(), ordersCacheKey).Val(); got == "" {
		t.Fatal("orders list should be cached after a miss")
	}

	// Invalidation is unconditional, so even stub-mode inserts clear it.
	req := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{"amount":1}`))
	crec := httptest.NewRecorder()
	handleOrdersCreate(crec, req)
	if err := rc.Get(context.Background(), ordersCacheKey).Err(); err != redis.Nil {
		t.Fatalf("orders cache key should be gone after a write, got err=%v", err)
	}
}

// TestNoRedisSkipsCaching keeps the original direct-to-DB behaviour when
// REDIS_ADDR is unset: handlers must not crash and are a pure no-op cache.
func TestNoRedisSkipsCaching(t *testing.T) {
	rc = nil

	rec := httptest.NewRecorder()
	handleOrdersList(rec)
	if !strings.Contains(rec.Body.String(), `"count":0`) {
		t.Fatalf("unexpected stub orders response: %q", rec.Body.String())
	}

	create := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{"amount":1}`))
	crec := httptest.NewRecorder()
	handleOrdersCreate(crec, create)
	if crec.Code != http.StatusOK {
		t.Fatalf("stub create should still succeed without redis, got %d", crec.Code)
	}
}
