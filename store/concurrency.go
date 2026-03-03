package store

import (
	"fmt"

	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
)

// ConcurrencyLimiter enforces per-key concurrency limits using PostgreSQL.
// Keys follow the pattern "kind:<step_kind>" or "entity:<field>:<value>".
type ConcurrencyLimiter struct {
	db *sqlx.DB
}

// NewConcurrencyLimiter connects to Postgres and returns a ConcurrencyLimiter.
func NewConcurrencyLimiter(dsn string) (*ConcurrencyLimiter, error) {
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if err := runEmbeddedMigrations(db, postgresMigrations, "migrate/postgres"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &ConcurrencyLimiter{db: db}, nil
}

// NewConcurrencyLimiterFromDB wraps an existing sqlx.DB connection.
func NewConcurrencyLimiterFromDB(db *sqlx.DB) *ConcurrencyLimiter {
	return &ConcurrencyLimiter{db: db}
}

// SetLimit upserts a concurrency limit for the given key.
func (cl *ConcurrencyLimiter) SetLimit(key string, maxConcurrent int) error {
	const query = `
		INSERT INTO concurrency_limits (key, max_concurrent, current_count)
		VALUES ($1, $2, 0)
		ON CONFLICT (key) DO UPDATE SET max_concurrent = EXCLUDED.max_concurrent`

	_, err := cl.db.Exec(query, key, maxConcurrent)
	if err != nil {
		return fmt.Errorf("set limit %q: %w", key, err)
	}
	return nil
}

// TryAcquire atomically increments current_count if below max_concurrent.
// Returns true if a slot was acquired, false if at capacity or key not found.
func (cl *ConcurrencyLimiter) TryAcquire(key string) bool {
	const query = `
		UPDATE concurrency_limits
		SET current_count = current_count + 1
		WHERE key = $1 AND current_count < max_concurrent`

	result, err := cl.db.Exec(query, key)
	if err != nil {
		return false
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false
	}
	return rows > 0
}

// Release decrements the current_count for the given key, flooring at zero.
func (cl *ConcurrencyLimiter) Release(key string) {
	const query = `
		UPDATE concurrency_limits
		SET current_count = GREATEST(current_count - 1, 0)
		WHERE key = $1`

	cl.db.Exec(query, key) //nolint:errcheck
}

// Close closes the underlying database connection.
func (cl *ConcurrencyLimiter) Close() error {
	return cl.db.Close()
}
