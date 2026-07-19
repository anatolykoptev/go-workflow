package workflow

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// dbAdvisoryLockKey is the shared cross-package DB test advisory lock key.
// Both the `store` and `workflow` test binaries use the SAME key so only one
// DB-backed test runs at a time across both packages. This copy lives in the
// internal `workflow` test package (listener_test.go is `package workflow`,
// not `workflow_test`) — the value MUST match dblock_test.go.
const dbAdvisoryLockKey = int64(0x57464744) // "WFGD"

// lockDB acquires a session-level Postgres advisory lock on a dedicated
// connection and holds it until t.Cleanup. Serializes DB-backed tests across
// the `store` and `workflow` test binaries. For listener tests this also
// prevents stray `pg_notify('step_done', ...)` broadcasts from the store
// package's StepQueue.Complete being delivered mid-test: a LISTEN started
// after the lock is acquired cannot see notifications sent before it.
func lockDB(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open advisory-lock conn: %v", err)
	}
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
