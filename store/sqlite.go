package store

import (
	"embed"
	"encoding/json"
	"fmt"

	workflow "github.com/anatolykoptev/go-workflow"

	"github.com/jmoiron/sqlx"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

//go:embed migrate/sqlite/*.sql
var sqliteMigrations embed.FS

// SQLiteBackend stores workflows in a SQLite database.
type SQLiteBackend struct {
	db *sqlx.DB
}

// NewSQLiteBackend creates a new SQLite-based backend at the given file path.
// The database is configured with WAL journal mode for better concurrency.
func NewSQLiteBackend(path string) (*SQLiteBackend, error) {
	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL"

	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite doesn't support concurrent writes

	if err := runSQLiteMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}

	return &SQLiteBackend{db: db}, nil
}

// NewSQLiteStore creates a WorkflowStore backed by SQLite at the given file path.
func NewSQLiteStore(path string) (*workflow.WorkflowStore, error) {
	backend, err := NewSQLiteBackend(path)
	if err != nil {
		return nil, err
	}
	return workflow.NewWorkflowStore(backend), nil
}

// Save persists a workflow using INSERT ... ON CONFLICT upsert.
func (s *SQLiteBackend) Save(w *workflow.Workflow) error {
	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("marshal workflow: %w", err)
	}

	const query = `
		INSERT INTO workflows (id, name, owner, state, idempotency_key, data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name            = excluded.name,
			owner           = excluded.owner,
			state           = excluded.state,
			idempotency_key = excluded.idempotency_key,
			data            = excluded.data,
			updated_at      = excluded.updated_at`

	_, err = s.db.Exec(query,
		w.ID, w.Name, w.Owner, string(w.State),
		nilIfEmpty(w.IdempotencyKey), string(data),
		w.CreatedAt, w.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("save workflow %s: %w", w.ID, err)
	}
	return nil
}

// Load retrieves a workflow by ID. Returns (nil, false) if not found.
func (s *SQLiteBackend) Load(id string) (*workflow.Workflow, bool) {
	var data string
	err := s.db.Get(&data, "SELECT data FROM workflows WHERE id = ?", id)
	if err != nil {
		return nil, false
	}

	var w workflow.Workflow
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		return nil, false
	}
	return &w, true
}

// Delete removes a workflow by ID. Returns nil even if the workflow doesn't exist.
func (s *SQLiteBackend) Delete(id string) error {
	_, err := s.db.Exec("DELETE FROM workflows WHERE id = ?", id)
	return err
}

// List returns workflows optionally filtered by state. Empty state returns all.
func (s *SQLiteBackend) List(state workflow.WorkflowState) []*workflow.Workflow {
	return s.queryWorkflows(state, "")
}

// ListByOwner returns workflows owned by the given owner.
func (s *SQLiteBackend) ListByOwner(owner string) []*workflow.Workflow {
	return s.queryWorkflows("", owner)
}

// FindByIdempotencyKey returns the first non-terminal workflow with the given key, or nil.
func (s *SQLiteBackend) FindByIdempotencyKey(key string) *workflow.Workflow {
	var data string
	err := s.db.Get(&data,
		"SELECT data FROM workflows WHERE idempotency_key = ? AND state NOT IN (?, ?, ?) LIMIT 1",
		key, string(workflow.StateCompleted), string(workflow.StateFailed), string(workflow.StateCancelled),
	)
	if err != nil {
		return nil
	}

	var w workflow.Workflow
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		return nil
	}
	return &w
}

// Modify atomically loads a workflow, applies fn, and saves it back within a transaction.
func (s *SQLiteBackend) Modify(id string, fn func(w *workflow.Workflow)) error {
	tx, err := s.db.Beginx()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var data string
	if err := tx.Get(&data, "SELECT data FROM workflows WHERE id = ?", id); err != nil {
		return fmt.Errorf("workflow %s not found", id)
	}

	var w workflow.Workflow
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		return fmt.Errorf("unmarshal workflow %s: %w", id, err)
	}

	fn(&w)

	updated, err := json.Marshal(&w)
	if err != nil {
		return fmt.Errorf("marshal workflow %s: %w", id, err)
	}

	const updateQuery = `
		UPDATE workflows SET
			name            = ?,
			owner           = ?,
			state           = ?,
			idempotency_key = ?,
			data            = ?,
			updated_at      = ?
		WHERE id = ?`

	_, err = tx.Exec(updateQuery,
		w.Name, w.Owner, string(w.State),
		nilIfEmpty(w.IdempotencyKey), string(updated),
		w.UpdatedAt, w.ID,
	)
	if err != nil {
		return fmt.Errorf("update workflow %s: %w", id, err)
	}

	return tx.Commit()
}

// Close closes the underlying database connection.
func (s *SQLiteBackend) Close() error {
	return s.db.Close()
}

// queryWorkflows builds a dynamic query with optional state and owner filters.
func (s *SQLiteBackend) queryWorkflows(state workflow.WorkflowState, owner string) []*workflow.Workflow {
	query := "SELECT data FROM workflows WHERE 1=1"
	var args []any

	if state != "" {
		query += " AND state = ?"
		args = append(args, string(state))
	}
	if owner != "" {
		query += " AND owner = ?"
		args = append(args, owner)
	}

	var rows []string
	if err := s.db.Select(&rows, query, args...); err != nil {
		return nil
	}

	result := make([]*workflow.Workflow, 0, len(rows))
	for _, data := range rows {
		var w workflow.Workflow
		if err := json.Unmarshal([]byte(data), &w); err != nil {
			continue
		}
		result = append(result, &w)
	}
	return result
}

// nilIfEmpty returns nil for an empty string, otherwise the string value.
// Used for nullable TEXT columns (e.g. idempotency_key).
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// runSQLiteMigrations applies embedded SQL migration files.
func runSQLiteMigrations(db *sqlx.DB) error {
	entries, err := sqliteMigrations.ReadDir("migrate/sqlite")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	for _, entry := range entries {
		sql, err := sqliteMigrations.ReadFile("migrate/sqlite/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if _, err := db.Exec(string(sql)); err != nil {
			return fmt.Errorf("exec %s: %w", entry.Name(), err)
		}
	}
	return nil
}
