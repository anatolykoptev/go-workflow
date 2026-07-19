package store_test

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DB-per-test isolation (go-test-setup Lever 2). Each DB-backed test gets its
// own freshly-created Postgres database, migrated by the store constructor,
// and dropped (WITH FORCE) in t.Cleanup. This replaces the old cross-package
// advisory-lock scheme (store/dblock_test.go): different databases never
// contend, so no lock is needed and there is no per-subtest connection churn.
//
// One admin connection (to the "postgres" maintenance DB) is opened lazily and
// reused for every CREATE/DROP — never a fresh connection per operation.

var (
	adminOnce sync.Once
	adminDB   *sql.DB
	adminErr  error
)

// requireTestDBName validates that dsn refers to a database whose name
// contains "_test". Returns a non-empty error string if the name looks like a
// production database.
func requireTestDBName(dsn string) string {
	if dsn == "" {
		return ""
	}
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		dbName := strings.TrimPrefix(u.Path, "/")
		if idx := strings.IndexByte(dbName, '?'); idx >= 0 {
			dbName = dbName[:idx]
		}
		if dbName != "" && !strings.Contains(dbName, "_test") {
			return fmt.Sprintf("refusing to connect: DB name %q must contain \"_test\" (set WORKFLOW_TEST_POSTGRES_DSN / GO_WORKFLOW_TEST_DSN to a test database)", dbName)
		}
		return ""
	}
	for _, part := range strings.Fields(dsn) {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 && kv[0] == "dbname" {
			if !strings.Contains(kv[1], "_test") {
				return fmt.Sprintf("refusing to connect: DB name %q must contain \"_test\" (set WORKFLOW_TEST_POSTGRES_DSN / GO_WORKFLOW_TEST_DSN to a test database)", kv[1])
			}
			return ""
		}
	}
	return ""
}

// rewriteDSNDBName returns a copy of dsn with the database name replaced by
// dbName. Handles both URL ("postgres://...") and key-value ("host=...") forms.
func rewriteDSNDBName(dsn, dbName string) (string, error) {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path = "/" + dbName
		return u.String(), nil
	}
	// Key-value form: replace dbname=..., or append if absent.
	fields := strings.Fields(dsn)
	found := false
	for i, part := range fields {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 && kv[0] == "dbname" {
			fields[i] = "dbname=" + dbName
			found = true
			break
		}
	}
	if !found {
		fields = append(fields, "dbname="+dbName)
	}
	return strings.Join(fields, " "), nil
}

// ensureAdminDB lazily opens (once per test binary) a single connection pool
// to the "postgres" maintenance DB, used for CREATE/DROP DATABASE. Returns the
// pool and a skip-worthy error if Postgres is unreachable.
func ensureAdminDB(t *testing.T, dsn string) (*sql.DB, error) {
	t.Helper()
	adminOnce.Do(func() {
		ad, err := rewriteDSNDBName(dsn, "postgres")
		if err != nil {
			adminErr = err
			return
		}
		db, err := sql.Open("pgx", ad)
		if err != nil {
			adminErr = err
			return
		}
		if err := db.Ping(); err != nil {
			_ = db.Close()
			adminErr = err
			return
		}
		db.SetMaxOpenConns(2)
		adminDB = db
	})
	return adminDB, adminErr
}

// newTestDB creates a fresh, isolated Postgres database for one test and
// returns its DSN. The database is dropped (WITH FORCE) in t.Cleanup. The
// store's embedded migrations are applied by NewPostgresBackend /
// NewConcurrencyLimiter when the test connects to the returned DSN.
//
// Skips the test if no test DSN is configured or Postgres is unreachable.
func newTestDB(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("WORKFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("GO_WORKFLOW_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("WORKFLOW_TEST_POSTGRES_DSN / GO_WORKFLOW_TEST_DSN not set")
	}
	if msg := requireTestDBName(dsn); msg != "" {
		t.Fatalf("test-DB isolation guard: %s", msg)
	}

	db, err := ensureAdminDB(t, dsn)
	if err != nil {
		t.Skip("postgres admin unavailable:", err)
	}

	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	name := "workflow_test_" + hex.EncodeToString(b[:])

	if _, err := db.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create test db %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
	})

	testDSN, err := rewriteDSNDBName(dsn, name)
	if err != nil {
		t.Fatalf("rewrite dsn for %s: %v", name, err)
	}
	return testDSN
}
