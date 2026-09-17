package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	_ "github.com/lib/pq"
)

// Demo HTTP app for barrage scenario testing, backed by Postgres when
// configured. Auth comes from the environment, never hardcoded:
//
//	POSTGRES_DSN=postgres://user:pass@localhost:5432/barrage_demo?sslmode=disable
//	(or DATABASE_URL, or PGUSER/PGPASSWORD/PGHOST/PGPORT/PGDATABASE parts)
//
// Without any of those the server still runs with stub responses so
// `go run ./cmd/demoserver` works with zero setup.
//
// Routes:
//
//	POST /api/login    -> {"token":"tok-123","user":{"id":42}}
//	GET  /api/me       -> checks Authorization: Bearer <token>
//	GET  /api/products -> product list (DB table when connected)
//	GET  /api/orders   -> {"orders":[...],"count":N} (DB table when connected)
//	POST /api/orders   -> INSERTs one row, returns {"id":N,...}
//	GET  /api/checkout -> echoes token query param
//	GET  /health       -> {"status":"ok","db":"connected|unconfigured"}
var db *sql.DB

func main() {
	db = openDBFromEnv()

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
	log.Println("routes:")
	log.Println("  POST /api/login     -> {\"token\":\"tok-123\"}")
	log.Println("  GET  /api/me        -> needs Authorization: Bearer <token>")
	log.Println("  GET  /api/products  -> list")
	log.Println("  POST /api/orders    -> create")
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

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token": "tok-123",
		"user":  map[string]any{"id": 42, "name": "alice"},
	})
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		http.Error(w, `{"error":"missing auth"}`, http.StatusUnauthorized)
		return
	}
	// expect Bearer tok-123
	token := strings.TrimPrefix(auth, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" || token == "{{token}}" {
		http.Error(w, `{"error":"bad token not interpolated"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":    42,
		"name":  "alice",
		"token": token,
	})
}

func handleProducts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if db == nil {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "name": "widget", "price": 9.99},
			{"id": 2, "name": "gadget", "price": 19.99},
		})
		return
	}
	rows, err := db.Query(`SELECT id, name, price FROM products ORDER BY id`)
	if err != nil {
		http.Error(w, `{"error":"db query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int
		var name, price string
		if err := rows.Scan(&id, &name, &price); err != nil {
			http.Error(w, `{"error":"db scan failed"}`, http.StatusInternalServerError)
			return
		}
		out = append(out, map[string]any{"id": id, "name": name, "price": price})
	}
	json.NewEncoder(w).Encode(out)
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

func handleOrdersList(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if db == nil {
		json.NewEncoder(w).Encode(map[string]any{"orders": []any{}, "count": 0})
		return
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM orders`).Scan(&count); err != nil {
		http.Error(w, `{"error":"db query failed"}`, http.StatusInternalServerError)
		return
	}
	rows, err := db.Query(`SELECT id, customer, amount FROM orders ORDER BY id DESC LIMIT 20`)
	if err != nil {
		http.Error(w, `{"error":"db query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	orders := []map[string]any{}
	for rows.Next() {
		var id int
		var customer, amount string
		if err := rows.Scan(&id, &customer, &amount); err != nil {
			http.Error(w, `{"error":"db scan failed"}`, http.StatusInternalServerError)
			return
		}
		orders = append(orders, map[string]any{"id": id, "customer": customer, "amount": amount})
	}
	json.NewEncoder(w).Encode(map[string]any{"orders": orders, "count": count})
}

// handleOrdersCreate inserts one row per call — the regular write op.
// The body is free-form JSON; customer falls back to "anon" and amount
// to 1 when absent, so canned barrage bodies keep working.
func handleOrdersCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
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
