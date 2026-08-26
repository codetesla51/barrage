package barrage

import (
	"testing"
	"time"
)

func TestIsReadQuery(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"SELECT count(*) FROM orders", true},
		{"  select customer from orders", true},
		{"SHOW TABLES", true},
		{"EXPLAIN ANALYZE SELECT 1", true},
		{"WITH recent AS (SELECT * FROM orders) SELECT count(*) FROM recent", true},
		{"  with recent AS (SELECT * FROM orders) DELETE FROM recent", true},
		{"INSERT INTO orders (customer, amount) VALUES ('x', 1)", false},
		{"INSERT INTO orders (customer) VALUES ('x') RETURNING id", false},
		{"INSERT INTO orders (customer) VALUES ('x') RETURNING id, created_at", false},
		{"UPDATE orders SET amount = 2 WHERE id = 1", false},
		{"DELETE FROM orders WHERE id = 1", false},
		{"DELETE FROM orders WHERE id = 1 RETURNING *", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isReadQuery(c.query); got != c.want {
			t.Errorf("isReadQuery(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestQueryIsRead(t *testing.T) {
	cases := []struct {
		name  string
		query QueryWeight
		want  bool
	}{
		{"explicit read wins over heuristic", QueryWeight{Query: "UPDATE orders SET x = 1", Type: "read"}, true},
		{"explicit write wins over heuristic", QueryWeight{Query: "SELECT count(*) FROM orders", Type: "write"}, false},
		{"mixed case explicit type", QueryWeight{Query: "SELECT 1", Type: "Read"}, true},
		{"unset type falls back to heuristic", QueryWeight{Query: "INSERT INTO orders (x) VALUES (1) RETURNING id"}, false},
		{"unset type falls back to heuristic read", QueryWeight{Query: "SELECT 1"}, true},
		{"unknown type falls back to heuristic", QueryWeight{Query: "SELECT 1", Type: "analyse"}, true},
	}
	for _, c := range cases {
		if got := queryIsRead(c.query); got != c.want {
			t.Errorf("%s: queryIsRead(%+v) = %v, want %v", c.name, c.query, got, c.want)
		}
	}
}

func TestBuildDBBucketsSubSecondWidthDoesNotPanic(t *testing.T) {
	// A bucketWidth under one second used to truncate to 0 in
	// int64(bucketWidth.Seconds()) and panic with a divide-by-zero. With the
	// clamp in place, sub-second and zero widths are treated as no bucketing
	// rather than crashing.
	samples := []dbQueryResult{
		{Timestamp: time.Unix(1, 0), Latency: 10 * time.Millisecond, Success: true},
		{Timestamp: time.Unix(2, 0), Latency: 20 * time.Millisecond, Success: true},
	}
	for _, w := range []time.Duration{0, 500 * time.Millisecond, time.Second} {
		// Must not panic.
		got := buildDBBuckets(samples, w)
		if w < time.Second && got != nil {
			t.Errorf("bucketWidth=%v: expected nil buckets, got %d", w, len(got))
		}
	}
}
