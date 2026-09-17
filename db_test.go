package barrage

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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
	// Bucketing uses UnixNano so sub-second widths bucket normally; zero or
	// negative widths return nil instead of panicking.
	samples := []dbQueryResult{
		{Timestamp: time.Unix(1, 0), Latency: 10 * time.Millisecond, Success: true},
		{Timestamp: time.Unix(2, 0), Latency: 20 * time.Millisecond, Success: true},
	}
	if got := buildDBBuckets(samples, 500*time.Millisecond); len(got) == 0 {
		t.Errorf("bucketWidth=500ms: expected buckets, got none")
	}
	if got := buildDBBuckets(samples, 0); got != nil {
		t.Errorf("bucketWidth=0: expected nil buckets, got %d", len(got))
	}
}

func TestEffectivePoolOptions(t *testing.T) {
	cases := []struct {
		name                       string
		target                     DBTarget
		concurrency                int
		wantOpen, wantIdle         int
		wantLifetime, wantIdleTime time.Duration
	}{
		{
			name:        "explicit values win",
			target:      DBTarget{MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: Duration(time.Minute), ConnMaxIdleTime: Duration(30 * time.Second)},
			concurrency: 20,
			wantOpen:    5, wantIdle: 2,
			wantLifetime: time.Minute, wantIdleTime: 30 * time.Second,
		},
		{
			name:        "unset counts fall back to concurrency",
			target:      DBTarget{},
			concurrency: 20,
			wantOpen:    20, wantIdle: 20,
		},
		{
			name:        "unset concurrency falls back to default",
			target:      DBTarget{},
			concurrency: 0,
			wantOpen:    DefaultConcurrency, wantIdle: DefaultConcurrency,
		},
		{
			name:        "explicit open caps default idle",
			target:      DBTarget{MaxOpenConns: 3},
			concurrency: 20,
			wantOpen:    3, wantIdle: 3,
		},
	}
	for _, c := range cases {
		gotOpen, gotIdle, gotLifetime, gotIdleTime := effectivePoolOptions(c.target, c.concurrency)
		if gotOpen != c.wantOpen || gotIdle != c.wantIdle || gotLifetime != c.wantLifetime || gotIdleTime != c.wantIdleTime {
			t.Errorf("%s: got (%d, %d, %v, %v), want (%d, %d, %v, %v)",
				c.name, gotOpen, gotIdle, gotLifetime, gotIdleTime,
				c.wantOpen, c.wantIdle, c.wantLifetime, c.wantIdleTime)
		}
	}
}

func TestApplyPoolOptionsSqlite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	applyPoolOptions(db, DBTarget{MaxOpenConns: 4, MaxIdleConns: 2}, 20)
	if got := db.Stats().MaxOpenConnections; got != 4 {
		t.Errorf("MaxOpenConnections = %d, want 4", got)
	}

	// defaults resolve to the worker count, never zero
	db2, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db2.Close()
	applyPoolOptions(db2, DBTarget{}, 7)
	if got := db2.Stats().MaxOpenConnections; got != 7 {
		t.Errorf("default MaxOpenConnections = %d, want 7", got)
	}
}
