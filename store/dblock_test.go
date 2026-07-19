package store_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// dbAdvisoryLockKey is a constant, shared key for the cross-package DB test
// advisory lock. Both the `store` and `workflow` test binaries use the SAME
// key so that only one DB-backed test runs at a time across both packages
// (Go runs test binaries in parallel by default, and they share one
// provisioned database).
const dbAdvisoryLockKey = int64(0x57464744) // "WFGD"

// lockDB acquires a session-level Postgres advisory lock on a dedicated
// connection and holds it until t.Cleanup. This serializes all DB-backed
// tests across the `store` and `workflow` test binaries, which otherwise run
// in parallel against the same database and race (e.g. one package's global
// CleanAll / scoped DELETE wiping another's parent workflow mid-Enqueue, or
// a List picking up another package's live workflow).
//
// The lock is session-level (not transaction-level) so it survives across the
// test's multiple connections/transactions; it is released on cleanup.
func lockDB(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open advisory-lock conn: %v", err)
	}
	// Pin to a single connection so the session-level lock stays on one conn
	// for the test's whole lifetime (the pool must not hand it out elsewhere).
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(),
		"SELECT pg_advisory_lock($1)", dbAdvisoryLockKey); err != nil {
		_ = db.Close()
		t.Fatalf("acquire advisory lock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(),
			"SELECT pg_advisory_unlock($1)", dbAdvisoryLockKey)
		_ = db.Close()
	})
}
