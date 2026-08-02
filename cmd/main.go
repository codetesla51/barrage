package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/codetesla51/barrage"
	_ "github.com/lib/pq"
)

var gotMethod string
var gotBody []byte
var gotHeader http.Header

func main() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	httpTarget := barrage.HTTPTarget{
		Method: "POST",
		URL:    server.URL,
		Body:   []byte(`{"foo":"bar"}`),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}
	dbTarget := barrage.DBTarget{
		Conn:   "postgres://us:2@localhost:5432/testDB?sslmode=disable",
		Driver: "postgres",
		Query: []barrage.QueryWeight{
			{Query: "SELECT 1", Weight: 70},
			{Query: "SELECT 2", Weight: 20},
			{Query: "SELECT 3", Weight: 10},
		},
		QueryType: "read",
	}
	cfg := barrage.OrchestratorConfig{
		Duration:    barrage.Duration(10 * time.Second),
		BucketWidth: barrage.Duration(1 * time.Second),
		HTTP: &barrage.HTTPRunnerConfig{
			Target: httpTarget,
			Rate:   10,
		},
		DB: &barrage.DBRunnerConfig{
			Target: dbTarget,
			Rate:   5,
		},
	}

	result, err := barrage.Orchestrator(cfg)
	if err != nil {
		panic(err)
	}
	fmt.Println("=== HTTP ===")
	fmt.Printf("Requests: %d\n", result.HTTPResult.Requests)
	fmt.Printf("Success rate: %.2f%%\n", result.HTTPResult.Success*100)
	fmt.Printf("p50: %s\n", result.HTTPResult.P50)
	fmt.Printf("p99: %s\n", result.HTTPResult.P99)
	fmt.Printf("Buckets: %d\n", len(result.HTTPResult.Buckets))
	for _, b := range result.HTTPResult.Buckets {
		fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
			b.Start.Format("15:04:05"), b.Requests, b.P50, b.P99)
	}

	fmt.Println("\n=== DB ===")
	fmt.Printf("Requests: %d\n", result.DBResult.Requests)
	fmt.Printf("Success rate: %.2f%%\n", result.DBResult.Success*100)
	fmt.Printf("p50: %s\n", result.DBResult.P50)
	fmt.Printf("p99: %s\n", result.DBResult.P99)
	fmt.Printf("Buckets: %d\n", len(result.DBResult.Buckets))
	for _, b := range result.DBResult.Buckets {
		fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
			time.Unix(b.Start, 0).Format("15:04:05"), b.Requests, b.P50, b.P99)
	}
}
