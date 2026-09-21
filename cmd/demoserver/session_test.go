package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSessionReuseSkipsBcrypt proves a successful login returns the SAME
// live token on repeat logins (no re-hash), and that the reused token
// still passes /api/me. This is the "don't recompute bcrypt" behavior the
// load test depends on.
func TestSessionReuseSkipsBcrypt(t *testing.T) {
	rc = nil
	sessions = sessionStore{byUser: make(map[string]sessionEntry)}

	login := func(body string) (int, string, bool) {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handleLogin(rec, req)
		var out struct {
			Token  string `json:"token"`
			Reused bool   `json:"reused"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.Token, out.Reused
	}

	code, t1, reused := login(`{"username":"alice","password":"secret"}`)
	if code != http.StatusOK || t1 == "" || reused {
		t.Fatalf("first login: code=%d token=%q reused=%v", code, t1, reused)
	}

	code, t2, reused := login(`{"username":"alice","password":"secret"}`)
	if code != http.StatusOK || t2 != t1 || !reused {
		t.Fatalf("second login should reuse the token, code=%d t2==t1=%v reused=%v", code, t2 == t1, reused)
	}

	// The reused token still validates end to end.
	me := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	me.Header.Set("Authorization", "Bearer "+t2)
	meRec := httptest.NewRecorder()
	handleMe(meRec, me)
	if meRec.Code != http.StatusOK {
		t.Fatalf("reused token should pass /api/me, got %d", meRec.Code)
	}
}

// TestFailedLoginsNeverCache proves wrong credentials always pay the full
// auth cost: a failed login must never plant a reusable session, and even
// a valid session never hands its token to a wrong password.
func TestFailedLoginsNeverCache(t *testing.T) {
	rc = nil
	sessions = sessionStore{byUser: make(map[string]sessionEntry)}

	login := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handleLogin(rec, req)
		return rec.Code
	}

	if code := login(`{"username":"alice","password":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong password should 401, got %d", code)
	}
	if _, ok := sessions.get("alice", "secret"); ok {
		t.Fatal("failed login must not leave a reusable session")
	}

	// After a successful login, a wrong password still 401s — the fast
	// session check rejects it and the full bcrypt path re-verifies.
	if code := login(`{"username":"alice","password":"secret"}`); code != http.StatusOK {
		t.Fatalf("good login should 200, got %d", code)
	}
	if _, ok := sessions.get("alice", "secret"); !ok {
		t.Fatal("successful login should leave a reusable session")
	}
	if _, ok := sessions.get("alice", "wrong"); ok {
		t.Fatal("wrong password must not satisfy the session check")
	}
	if code := login(`{"username":"alice","password":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong password must still 401 despite a valid session, got %d", code)
	}
}

// TestExpiredSessionForcesReauth: once the token TTL is up, the next login
// re-authenticates and issues a fresh token instead of reusing the dead one.
func TestExpiredSessionForcesReauth(t *testing.T) {
	rc = nil
	sessions = sessionStore{byUser: make(map[string]sessionEntry)}

	sessions.put("alice", sessionEntry{
		uid: 42, name: "alice", token: "stale-token", exp: time.Now().Add(-time.Second),
	})
	if _, ok := sessions.get("alice", "secret"); ok {
		t.Fatal("expired session should be dropped by get")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"alice","password":"secret"}`))
	rec := httptest.NewRecorder()
	handleLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after expiry should re-auth fine, got %d", rec.Code)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Token == "" || out.Token == "stale-token" {
		t.Fatalf("expected a fresh token, got %q", out.Token)
	}
}