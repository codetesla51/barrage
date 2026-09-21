package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost tunes the demo's login realism against the 2-vCPU runner:
// cost 8 is a real password hash that logins can still verify quickly.
// Production defaults to 10-12, which is deliberately CPU-expensive.
const bcryptCost = 8

// Seeds the demo `orders` table with a large number of rows so read/write
// queries during a load test have real work to do. Uses COPY for bulk insert.
func defaultConn() string {
	if dsn := strings.TrimSpace(os.Getenv("POSTGRES_DSN")); dsn != "" {
		return dsn
	}
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return dsn
	}
	return "postgres://us:2@localhost:5432/testDB?sslmode=disable"
}

func main() {
	conn := flag.String("conn", defaultConn(), "postgres DSN (or POSTGRES_DSN env)")
	rows := flag.Int("n", 1_000_000, "number of rows to insert")
	reset := flag.Bool("reset", true, "drop and recreate the orders table first")
	flag.Parse()

	db, err := sql.Open("postgres", *conn)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("ping: %v", err)
	}

	if *reset {
		if _, err := db.Exec("DROP TABLE IF EXISTS orders"); err != nil {
			log.Fatalf("drop: %v", err)
		}
		if _, err := db.Exec(`CREATE TABLE orders (
			id serial PRIMARY KEY,
			customer text NOT NULL,
			amount numeric(10,2) NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
			log.Fatalf("create: %v", err)
		}
	}

	start := time.Now()
	const chunk = 100_000
	for startIdx := 0; startIdx < *rows; startIdx += chunk {
		end := min(startIdx+chunk, *rows)
		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("begin: %v", err)
		}
		stmt, err := tx.Prepare(pq.CopyIn("orders", "customer", "amount", "created_at"))
		if err != nil {
			log.Fatalf("prepare: %v", err)
		}
		for i := startIdx; i < end; i++ {
			customer := fmt.Sprintf("customer-%d", i%10_000)
			amount := fmt.Sprintf("%.2f", rand.Float64()*1000)
			created := time.Now().Add(-time.Duration(rand.Intn(365*24)) * time.Hour)
			if _, err := stmt.Exec(customer, amount, created); err != nil {
				log.Fatalf("exec: %v", err)
			}
		}
		if _, err := stmt.Exec(); err != nil {
			log.Fatalf("end copy: %v", err)
		}
		if err := stmt.Close(); err != nil {
			log.Fatalf("close stmt: %v", err)
		}
		if err := tx.Commit(); err != nil {
			log.Fatalf("commit: %v", err)
		}
		fmt.Printf("\rinserted %d rows", end)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM orders").Scan(&count); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("\rseeded %d rows in %s (total in table: %d)\n", *rows, time.Since(start).Round(time.Millisecond), count)

	if err := addOrdersIndexes(db); err != nil {
		log.Fatalf("orders index: %v", err)
	}
	if err := seedUsers(db); err != nil {
		log.Fatalf("users: %v", err)
	}
	fmt.Println("indexes + users ready")
}

// addOrdersIndexes makes the demo's read paths index-only: newest-orders
// uses the PK, per-customer pages use (customer, id DESC), and the
// recent-window feed uses created_at. Without these, growing the table to
// 1M rows would turn every read into a full scan.
func addOrdersIndexes(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_orders_created_at ON orders (created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_customer_id ON orders (customer, id DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// seedUsers creates the login table and the demo accounts the scenario
// profiles authenticate as. Hashes are real bcrypt (see bcryptCost).
func seedUsers(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id         bigserial PRIMARY KEY,
		username   text NOT NULL UNIQUE,
		pass_hash  text NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create users: %w", err)
	}
	for _, u := range []struct{ name, pass string }{
		{"alice", "secret"},
		{"bob", "secret"},
		{"carol", "secret"},
	} {
		hash, err := bcrypt.GenerateFromPassword([]byte(u.pass), bcryptCost)
		if err != nil {
			return fmt.Errorf("bcrypt %s: %w", u.name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO users (username, pass_hash) VALUES ($1, $2) ON CONFLICT (username) DO NOTHING`,
			u.name, string(hash),
		); err != nil {
			return fmt.Errorf("insert %s: %w", u.name, err)
		}
	}
	return nil
}
