// pgschema ensures the HA-benchmark table exists for the berth write sweep.
// Runs only when the sweep workflow is dispatched with include_writes=yes.
// Usage: go run ./profiles/pgschema "<postgres dsn>"
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/lib/pq"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pgschema <dsn>")
		os.Exit(1)
	}
	db, err := sql.Open("postgres", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS public.barrage_load (
		id         bigserial PRIMARY KEY,
		payload    text NOT NULL,
		created_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		os.Exit(1)
	}
	fmt.Println("barrage_load ready")
}