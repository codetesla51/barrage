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

	target := barrage.Tareget{
		Method: "POST",
		URL:    server.URL,
		Body:   []byte(`{"foo":"bar"}`),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	result, err := barrage.FireHTTP(target, 10, 3*time.Second, 1*time.Second)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Printf("Requests: %d\n", result.Requests)
	fmt.Printf("Success rate: %.2f%%\n", result.Success*100)
	fmt.Printf("p50: %s\n", result.P50)
	fmt.Printf("p99: %s\n", result.P99)
	fmt.Printf("Buckets: %d\n", len(result.Buckets))

	for _, b := range result.Buckets {
		fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
			b.Start.Format("15:04:05"), b.Requests, b.P50, b.P99)
	}

	dbTarget := barrage.Target{
		Conn:   "postgres://us:2@localhost:5432/testDB?sslmode=disable",
		Driver: "postgres",
		Query: []barrage.QueryWeight{
			{Query: "SELECT 1", Weight: 70},
			{Query: "SELECT 2", Weight: 20},
			{Query: "SELECT 3", Weight: 10},
		},
		QueryType: "read",
	}

	dbResult, err := barrage.FireDB(dbTarget, 10, 3*time.Second, 1*time.Second)
	if err != nil {
		fmt.Println("DB error:", err)
		return
	}

	fmt.Println("\n=== DB ===")
	fmt.Printf("Requests: %d\n", dbResult.Requests)
	fmt.Printf("Success rate: %.2f%%\n", dbResult.Success*100)
	fmt.Printf("p50: %s\n", dbResult.P50)
	fmt.Printf("p99: %s\n", dbResult.P99)
	fmt.Printf("Buckets: %d\n", len(dbResult.Buckets))
	for _, b := range dbResult.Buckets {
		fmt.Printf("  [%s] requests=%d p50=%s p99=%s\n",
			time.Unix(b.Start, 0).Format("15:04:05"), b.Requests, b.P50, b.P99)
	}
}
