package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// Demo HTTP application serving the orders API. No artificial latency is
// injected; responses are as fast as the handler allows.
func main() {
	http.HandleFunc("/api/orders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "customer": 42, "status": "ok"})
	})
	log.Println("demo server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
