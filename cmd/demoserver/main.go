package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Demo HTTP endpoint that produces deterministic latency spikes so the
// correlation report has data to show. Every 5th wall-clock second it sleeps
// 250ms; otherwise it answers quickly.
func main() {
	http.HandleFunc("/api/orders", func(w http.ResponseWriter, r *http.Request) {
		if time.Now().Unix()%5 == 1 {
			time.Sleep(250 * time.Millisecond)
		} else {
			time.Sleep(3 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "status": "ok"})
	})
	log.Println("demo server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
