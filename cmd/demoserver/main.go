package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// Demo HTTP app for barrage scenario testing, backed by Postgres when
// configured. Written to behave like a real backend so the capacity tool
// teaches real lessons:
//
//   - login verifies a bcrypt hash and issues an HMAC-signed token
//     (AUTH_SECRET key, 15m expiry); /api/me validates it. Repeat logins
//     reuse the live session instead of re-running bcrypt.
//   - read routes use indexes only — the orders list hits the PRIMARY KEY
//     and reads the newest 20 rows, never the 1M-row table.
//   - products/orders list are served from Redis (5s TTL) when REDIS_ADDR
//     is set; creating an order invalidates the orders key.
//
// Without POSTGRES_DSN the server runs with stub responses so
// `go run ./cmd/demoserver` works with zero setup (login still issues a
// real signed token for the demo account alice/secret).
//
// Routes:
//
//	POST /api/login     -> {"token":"<signed>","user":{...}}  (bcrypt, then session reuse)
//	GET  /api/me        -> validates Bearer <signed token>
//	GET  /api/products  -> product list (indexed, cached)
//	GET  /api/orders    -> {"orders":[...]} newest 20 (indexed, cached)
//	POST /api/orders    -> INSERTs one row, invalidates the orders cache
//	GET  /api/checkout  -> echoes token query param
//	GET  /health        -> {"status":"ok","db":"connected|unconfigured"}
var db *sql.DB
var rc *redis.Client

const cacheTTL = 5 * time.Second
const productsCacheKey = "cache:products"
const ordersCacheKey = "cache:orders"
const tokenTTL = 15 * time.Minute

// sessionStore keeps a user's live token across repeat logins so the
// bcrypt hash runs once per session, not once per login request — the
// same thing a real backend does by keeping the client's session alive.
// The accepted password is remembered as a fast HMAC check, so a repeat
// login re-verifies in microseconds without touching bcrypt, while a
// wrong password still falls through to the full bcrypt path and 401s.
var sessions = sessionStore{byUser: make(map[string]sessionEntry)}

type sessionStore struct {
	mu     sync.Mutex
	byUser map[string]sessionEntry
}

type sessionEntry struct {
	uid    int
	name   string
	passOK [32]byte // fast HMAC of the accepted password (not the hash)
	token  string
	exp    time.Time
}

// get returns the live session for user when the presented password still
// matches (fast check) — otherwise false, so the caller re-authenticates
// through the full bcrypt path. An expired entry is dropped.
func (s *sessionStore) get(user, password string) (sessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byUser[user]
	if !ok {
		return sessionEntry{}, false
	}
	if time.Now().After(e.exp) {
		delete(s.byUser, user)
		return sessionEntry{}, false
	}
	want := sessionCheck(password)
	if subtle.ConstantTimeCompare(want[:], e.passOK[:]) != 1 {
		return sessionEntry{}, false
	}
	return e, true
}

func (s *sessionStore) put(user string, e sessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byUser[user] = e
}

// sessionCheck derives the fast password fingerprint carried in a session.
// Keyed by the auth secret (domain-separated from token signing) so the
// fingerprint is unguessable and collisions with other HMAC uses are moot.
func sessionCheck(password string) [32]byte {
	mac := hmac.New(sha256.New, authSecret())
	mac.Write([]byte("session-check:"))
	mac.Write([]byte(password))
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

// authSecret returns the HMAC key for login tokens. AUTH_SECRET must be
// set anywhere real; the dev default keeps zero-setup runs working.
func authSecret() []byte {
	if s := strings.TrimSpace(os.Getenv("AUTH_SECRET")); s != "" {
		return []byte(s)
	}
	return []byte("dev-secret-change-me")
}

func main() {
	db = openDBFromEnv()
	rc = openRedisFromEnv()

	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/me", handleMe)
	http.HandleFunc("/api/products", handleProducts)
	http.HandleFunc("/api/orders", handleOrders)
	http.HandleFunc("/api/checkout", handleCheckout)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/", handleRoot)

	log.Println("demo server listening on :8080")
	if db != nil {
		log.Println("db: connected (postgres)")
	} else {
		log.Println("db: unconfigured, using stub responses (set POSTGRES_DSN to use postgres)")
	}
	if rc != nil {
		log.Printf("redis: connected (%s), caching products/orders for %s", rc.Options().Addr, cacheTTL)
	} else {
		log.Println("redis: unconfigured, no caching (set REDIS_ADDR to enable)")
	}
	log.Println("routes:")
	log.Println("  POST /api/login     -> bcrypt verify, then session reuse")
	log.Println("  GET  /api/me        -> validates Bearer <signed token>")
	log.Println("  GET  /api/products  -> list (cached)")
	log.Println("  GET  /api/orders    -> newest 20 (cached, index-scanned)")
	log.Println("  POST /api/orders    -> create (invalidates list cache)")
	log.Println("  GET  /api/checkout?token={{token}} -> echo")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// postgresDSN resolves the connection string from the environment.
// Empty means "not configured" — the caller falls back to stubs.
func postgresDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("POSTGRES_DSN")); dsn != "" {
		return dsn
	}
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return dsn
	}
	user := strings.TrimSpace(os.Getenv("PGUSER"))
	if user == "" {
		return ""
	}
	host := getenv("PGHOST", "localhost")
	port := getenv("PGPORT", "5432")
	name := getenv("PGDATABASE", "barrage_demo")
	pass := os.Getenv("PGPASSWORD")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(user), url.QueryEscape(pass), host, port, name)
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// openDBFromEnv connects when configured, else returns nil so handlers
// serve stubs. A bad DSN is fatal — silent fallback would hide misconfig
// and send load at stubs while you think you are testing the database.
func openDBFromEnv() *sql.DB {
	dsn := postgresDSN()
	if dsn == "" {
		return nil
	}
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	if err := conn.Ping(); err != nil {
		log.Fatalf("db ping: %v (check POSTGRES_DSN / PG* env)", err)
	}
	return conn
}

// openRedisFromEnv connects when REDIS_ADDR is set, else returns nil so
// the read handlers go straight to the database. A bad address is fatal,
// same rationale as the DB: silently ignoring a configured cache would
// make the clock lie about which layer served the reads.
func openRedisFromEnv() *redis.Client {
	addr := strings.TrimSpace(os.Getenv("REDIS_ADDR"))
	if addr == "" {
		return nil
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v (check REDIS_ADDR)", err)
	}
	return c
}

// serveCached answers a JSON read route: serve the cached bytes on a hit,
// otherwise render via gen and store the result under key for ttl. gen's
// error is written back as a 500. rc == nil disables caching entirely.
func serveCached(w http.ResponseWriter, key string, ttl time.Duration, gen func() ([]byte, error)) {
	if rc != nil {
		if b, err := rc.Get(context.Background(), key).Bytes(); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
			return
		}
	}
	b, err := gen()
	if err != nil {
		http.Error(w, `{"error":`+strconv.Quote(err.Error())+`}`, http.StatusInternalServerError)
		return
	}
	if rc != nil {
		rc.Set(context.Background(), key, b, ttl)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// issueToken signs a short-lived claim so /api/me can trust it without a
// session store — the same shape real backends use (JWT-class mechanics).
func issueToken(uid int, name string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"uid":  uid,
		"name": name,
		"exp":  time.Now().Add(tokenTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, authSecret())
	mac.Write([]byte(b64))
	return b64 + "." + fmt.Sprintf("%x", mac.Sum(nil)), nil
}

// parseToken checks signature and expiry, returning the claim's uid+name.
func parseToken(tok string) (int, string, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("malformed token")
	}
	b64, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, authSecret())
	mac.Write([]byte(b64))
	want := fmt.Sprintf("%x", mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return 0, "", fmt.Errorf("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return 0, "", err
	}
	var claims struct {
		UID  int    `json:"uid"`
		Name string `json:"name"`
		Exp  int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return 0, "", err
	}
	if time.Now().Unix() > claims.Exp {
		return 0, "", fmt.Errorf("expired")
	}
	return claims.UID, claims.Name, nil
}

func bearerToken(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// Session reuse: a previously logged-in user with the right password
	// keeps their live token — a fast HMAC check, no bcrypt, no
	// auth-store lookup. A wrong password falls through to authenticate.
	if sess, ok := sessions.get(body.Username, body.Password); ok {
		json.NewEncoder(w).Encode(map[string]any{
			"token":  sess.token,
			"user":   map[string]any{"id": sess.uid, "name": sess.name},
			"reused": true,
		})
		return
	}

	uid, name, err := authenticate(body.Username, body.Password)
	if err != nil {
		// Same 401 either way — a login endpoint never leaks which part failed.
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	tok, err := issueToken(uid, name)
	if err != nil {
		http.Error(w, `{"error":"token issue failed"}`, http.StatusInternalServerError)
		return
	}
	sessions.put(body.Username, sessionEntry{
		uid: uid, name: name, passOK: sessionCheck(body.Password), token: tok, exp: time.Now().Add(tokenTTL),
	})
	json.NewEncoder(w).Encode(map[string]any{
		"token": tok,
		"user":  map[string]any{"id": uid, "name": name},
	})
}

// authenticate checks credentials against the users table (bcrypt). In
// stub mode (no DB) only the demo account works. hashAndPassword stays in
// the select so CompareHashAndPassword reads the value out of Postgres.
func authenticate(username, password string) (int, string, error) {
	if db == nil {
		if username == "alice" && password == "secret" {
			return 42, "alice", nil
		}
		return 0, "", fmt.Errorf("invalid credentials")
	}
	var (
		uid  int
		name string
		hash string
	)
	err := db.QueryRow(
		`SELECT id, username, pass_hash FROM users WHERE username = $1`,
		username,
	).Scan(&uid, &name, &hash)
	if err == sql.ErrNoRows {
		return 0, "", fmt.Errorf("invalid credentials")
	}
	if err != nil {
		return 0, "", err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return 0, "", fmt.Errorf("invalid credentials")
	}
	return uid, name, nil
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	tok := bearerToken(r)
	if tok == "" || tok == "{{token}}" {
		http.Error(w, `{"error":"bad token not interpolated"}`, http.StatusUnauthorized)
		return
	}
	uid, name, err := parseToken(tok)
	if err != nil {
		http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id": uid, "name": name, "token": tok})
}

// productsList renders the products JSON, hitting the DB when connected.
func productsList() ([]byte, error) {
	var payload any
	if db == nil {
		payload = []map[string]any{
			{"id": 1, "name": "widget", "price": 9.99},
			{"id": 2, "name": "gadget", "price": 19.99},
		}
	} else {
		rows, err := db.Query(`SELECT id, name, price::float8 FROM products ORDER BY id`)
		if err != nil {
			return nil, fmt.Errorf("products query: %w", err)
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id int
			var name string
			var price float64
			if err := rows.Scan(&id, &name, &price); err != nil {
				return nil, fmt.Errorf("products scan: %w", err)
			}
			out = append(out, map[string]any{"id": id, "name": name, "price": price})
		}
		payload = out
	}
	return json.Marshal(payload)
}

func handleProducts(w http.ResponseWriter, r *http.Request) {
	serveCached(w, productsCacheKey, cacheTTL, productsList)
}

func handleOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		handleOrdersList(w)
		return
	}
	if r.Method == http.MethodPost {
		handleOrdersCreate(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// ordersList returns the newest 20 orders. The ORDER BY id DESC hits the
// primary-key index, so it touches 20 rows — deliberately NOT a
// `SELECT count(*)` over the 1M-row table, which is what real apps avoid.
func ordersList() ([]byte, error) {
	if db == nil {
		return json.Marshal(map[string]any{"orders": []any{}})
	}
	rows, err := db.Query(
		`SELECT id, customer, amount::float8, created_at FROM orders ORDER BY id DESC LIMIT 20`)
	if err != nil {
		return nil, fmt.Errorf("orders query: %w", err)
	}
	defer rows.Close()
	orders := []map[string]any{}
	for rows.Next() {
		var id int
		var customer string
		var amount float64
		var created time.Time
		if err := rows.Scan(&id, &customer, &amount, &created); err != nil {
			return nil, fmt.Errorf("orders scan: %w", err)
		}
		orders = append(orders, map[string]any{
			"id":         id,
			"customer":   customer,
			"amount":     amount,
			"created_at": created.Format(time.RFC3339),
		})
	}
	return json.Marshal(map[string]any{"orders": orders})
}

func handleOrdersList(w http.ResponseWriter) {
	serveCached(w, ordersCacheKey, cacheTTL, ordersList)
}

// handleOrdersCreate inserts one row per call — the regular write op.
// The body is free-form JSON; customer falls back to "anon" and amount
// to 1 when absent, so canned barrage bodies keep working.
func handleOrdersCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Any order creation invalidates the cached list — pessimistic
	// (unconditional) invalidation, simplest to reason about. Runs even in
	// stub mode so the cache never outlives a write.
	if rc != nil {
		rc.Del(context.Background(), ordersCacheKey)
	}
	if db == nil {
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "customer": 42, "status": "ok"})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	customer := "anon"
	if v, ok := body["customer"]; ok && v != nil {
		customer = fmt.Sprint(v)
	}
	amount := 1.0
	if v, ok := body["amount"]; ok {
		switch n := v.(type) {
		case float64:
			amount = n
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
				amount = f
			}
		}
	}
	var id int
	err := db.QueryRow(
		`INSERT INTO orders (customer, amount) VALUES ($1, $2) RETURNING id`,
		customer, amount,
	).Scan(&id)
	if err != nil {
		http.Error(w, `{"error":"db insert failed"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id": id, "customer": customer, "status": "ok"})
}

func handleCheckout(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	auth := r.Header.Get("Authorization")
	w.Header().Set("Content-Type", "application/json")
	if token == "" && auth == "" {
		http.Error(w, `{"error":"missing token"}`, http.StatusBadRequest)
		return
	}
	// if token still has {{}} it was not interpolated
	if strings.Contains(token, "{{") {
		http.Error(w, `{"error":"token not interpolated"}`, http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"status": "checked_out",
		"token":  token,
		"auth":   auth,
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	state := "unconfigured"
	if db != nil {
		state = "connected"
	}
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "db": state})
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "barrage demo server - try /health, /api/login, /api/me, /api/products, /api/orders, /api/checkout")
}
