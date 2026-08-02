package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/lib/pq"
)

// Seeds the demo `orders` table with a large number of rows so read/write
// queries during a load test have real work to do. Uses COPY for bulk insert.
func main() {
	conn := flag.String("conn", "postgres://us:2@localhost:5432/testDB?sslmode=disable", "postgres DSN")
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
}
