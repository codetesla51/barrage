package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// Demo HTTP app for barrage scenario testing.
// Routes:
//   POST /api/login    -> {"token":"tok-123","user":{"id":42}}
//   GET  /api/me       -> checks Authorization: Bearer <token>
//   GET  /api/products -> [{"id":1,"name":"widget"}]
//   POST /api/orders   -> {"id":1,"customer":42,"status":"ok"}
//   GET  /api/checkout -> echoes token query param
func main() {
	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/me", handleMe)
	http.HandleFunc("/api/products", handleProducts)
	http.HandleFunc("/api/orders", handleOrders)
	http.HandleFunc("/api/checkout", handleCheckout)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/", handleRoot)

	log.Println("demo server listening on :8080")
	log.Println("routes:")
	log.Println("  POST /api/login     -> {\"token\":\"tok-123\"}")
	log.Println("  GET  /api/me        -> needs Authorization: Bearer <token>")
	log.Println("  GET  /api/products  -> list")
	log.Println("  POST /api/orders    -> create")
	log.Println("  GET  /api/checkout?token={{token}} -> echo")
	log.Fatal(http.ListenAndServe(":8080", nil))
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
	json.NewEncoder(w).Encode([]map[string]any{
		{"id": 1, "name": "widget", "price": 9.99},
		{"id": 2, "name": "gadget", "price": 19.99},
	})
}

func handleOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"orders": []any{}, "count": 0})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"id": 1, "customer": 42, "status": "ok"})
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
	json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "barrage demo server - try /health, /api/login, /api/me, /api/products, /api/orders, /api/checkout")
}
