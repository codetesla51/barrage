package barrage

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFireHTTP_WithMethodBodyHeaders(t *testing.T) {
	var gotMethod string
	var gotBody []byte
	var gotHeader http.Header

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	target := Tareget{
		Method: "POST",
		URL:    ts.URL,
		Body:   []byte(`{"foo":"bar"}`),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	result, err := FireHTTP(target, 10, 1*time.Second, 1*time.Second)
	if err != nil {
		t.Fatalf("FireHTTP returned error: %v", err)
	}

	if gotMethod != "POST" {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if string(gotBody) != `{"foo":"bar"}` {
		t.Errorf("unexpected body: %s", gotBody)
	}
	if gotHeader.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type header, got %v", gotHeader)
	}
	if result.Requests == 0 {
		t.Errorf("expected requests to be fired")
	}
	if len(result.Buckets) == 0 {
		t.Errorf("expected at least one bucket to be populated")
	}
}
func TestPercentile(t *testing.T) {
	data := []time.Duration{
		10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond,
		40 * time.Millisecond, 50 * time.Millisecond,
	}
	got := percentile(data, 0.50)
	// 5 items, p=0.50 → idx = int(0.5*5) = 2 → data[2] = 30ms
	if got != 30*time.Millisecond {
		t.Errorf("got %v, want 30ms", got)
	}
}
func TestPercentileEmpty(t *testing.T) {
	data := []time.Duration{}
	got := percentile(data, 0.95)
	if got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}
func TestBuildDbBuckets(t *testing.T) {
	runStart := time.Now()
	results := []queryResult{
		{Timestamp: runStart.Add(300 * time.Millisecond), Success: true, Latency: 5 * time.Millisecond},   // A
		{Timestamp: runStart.Add(700 * time.Millisecond), Success: true, Latency: 8 * time.Millisecond},   // B
		{Timestamp: runStart.Add(1200 * time.Millisecond), Success: true, Latency: 3 * time.Millisecond},  // C
		{Timestamp: runStart.Add(2900 * time.Millisecond), Success: false, Latency: 9 * time.Millisecond}, // D
	}

	buckets := buildDbBuckets(results, runStart, time.Second)

	if len(buckets) != 3 {
		t.Fatalf("expected %d buckets, got %d", 4, len(buckets))
	}
	if buckets[0].Requests != 2 {
		t.Errorf("bucket 0: expected %d requests, got %d", 0, buckets[0].Requests)
	}
	if buckets[1].Requests != 1 {
		t.Errorf("bucket 1: expected %d requests, got %d", 0, buckets[1].Requests)
	}
}
