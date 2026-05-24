// Example: clickhouse-go over native TCP to ClickHouse (not through clickhouse-bulk).
//
// Use this pattern for application writes that need driver semantics (errors, types,
// PrepareBatch). Point high-volume ETL/agents at bulk :8124 separately if needed.
//
// Run: cd examples && go run go_direct_ch.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := env("CH_ADDR", "127.0.0.1:9000")
	db := env("CH_DATABASE", "default")
	user := env("CH_USER", "default")
	pass := env("CH_PASSWORD", "")

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{
			Database: db,
			Username: user,
			Password: pass,
		},
		// Native protocol (default). Do not set Protocol: HTTP when talking to bulk.
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := conn.Ping(ctx); err != nil {
		log.Fatalf("ping: %v", err)
	}

	const ddl = `
CREATE TABLE IF NOT EXISTS bulk_example_events (
    id   UInt64,
    msg  String,
    ts   DateTime DEFAULT now()
) ENGINE = MergeTree ORDER BY id`
	if err := conn.Exec(ctx, ddl); err != nil {
		log.Fatalf("ddl: %v", err)
	}

	batch, err := conn.PrepareBatch(ctx, "INSERT INTO bulk_example_events (id, msg)")
	if err != nil {
		log.Fatalf("prepare batch: %v", err)
	}
	if err := batch.Append(uint64(1), "from clickhouse-go native"); err != nil {
		log.Fatalf("append: %v", err)
	}
	if err := batch.Send(); err != nil {
		log.Fatalf("send: %v", err)
	}

	var count uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM bulk_example_events").Scan(&count); err != nil {
		log.Fatalf("count: %v", err)
	}
	fmt.Printf("rows in bulk_example_events: %d\n", count)
}
