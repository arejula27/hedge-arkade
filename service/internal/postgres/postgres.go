// Package postgres is the storage adapter. It is the only package that knows
// SQL exists; everything above it works in domain types.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DB is a connection pool.
type DB struct {
	pool *sql.DB
}

// Open dials the database and confirms it answers. sql.Open alone does not
// connect, so a bad address would otherwise surface on the first request rather
// than at startup.
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the database: %w", err)
	}

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}

	return &DB{pool: pool}, nil
}

func (db *DB) Close() error { return db.pool.Close() }

// Health is what the readiness endpoint reports.
type Health struct {
	Up              bool   `json:"up"`
	Error           string `json:"error,omitempty"`
	OpenConnections int    `json:"open_connections"`
	InUse           int    `json:"in_use"`
	Idle            int    `json:"idle"`
	WaitCount       int64  `json:"wait_count"`
	WaitDuration    string `json:"wait_duration"`
}

// Check pings the database and reports the pool's counters.
//
// It never terminates the process: a database that is down is exactly the case
// a health check exists to report.
func (db *DB) Check(ctx context.Context) Health {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	if err := db.pool.PingContext(ctx); err != nil {
		return Health{Up: false, Error: err.Error()}
	}

	stats := db.pool.Stats()
	return Health{
		Up:              true,
		OpenConnections: stats.OpenConnections,
		InUse:           stats.InUse,
		Idle:            stats.Idle,
		WaitCount:       stats.WaitCount,
		WaitDuration:    stats.WaitDuration.String(),
	}
}
