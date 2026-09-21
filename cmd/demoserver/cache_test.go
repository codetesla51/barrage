package main

import (
	"context"
	"encoding/json"
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
	if !strings.Contains(rec.Body.String(), `"orders":[]`) {
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
	if !strings.Contains(rec.Body.String(), `"orders":[]`) {
		t.Fatalf("unexpected stub orders response: %q", rec.Body.String())
	}

	create := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{"amount":1}`))
	crec := httptest.NewRecorder()
	handleOrdersCreate(crec, create)
	if crec.Code != http.StatusOK {
		t.Fatalf("stub create should still succeed without redis, got %d", crec.Code)
	}
}

// TestLoginAndTokenFlow checks the realistic auth path in stub mode: a
// bcrypt-checked login (demo account only), a signed token that /api/me
// validates, and a 401 on tampering or wrong credentials.
func TestLoginAndTokenFlow(t *testing.T) {
	rc = nil

	login := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handleLogin(rec, req)
		return rec
	}

	// Wrong password -> 401, same error shape as unknown user.
	if rec := login(`{"username":"alice","password":"wrong"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password should 401, got %d", rec.Code)
	}
	if rec := login(`{"username":"mallory","password":"secret"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user should 401, got %d", rec.Code)
	}

	// Good login -> 200 with a token.
	rec := login(`{"username":"alice","password":"secret"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("good login should 200, got %d", rec.Code)
	}
	var got struct {
		Token string `json:"token"`
		User  struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad login json: %v", err)
	}
	if got.Token == "" || got.User.ID != 42 || got.User.Name != "alice" {
		t.Fatalf("unexpected login payload: %+v", got)
	}

	// /api/me accepts the signed token...
	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+got.Token)
	meRec := httptest.NewRecorder()
	handleMe(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("valid token should 200, got %d", meRec.Code)
	}

	// ...and rejects one that has been tampered with.
	meReq2 := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq2.Header.Set("Authorization", "Bearer "+got.Token+"x")
	meRec2 := httptest.NewRecorder()
	handleMe(meRec2, meReq2)
	if meRec2.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token should 401, got %d", meRec2.Code)
	}
}
