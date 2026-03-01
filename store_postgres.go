package workflow

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
)

//go:embed migrate/postgres/*.sql
var postgresMigrations embed.FS

// PostgresBackend stores workflows in a PostgreSQL database using JSONB.
type PostgresBackend struct {
	db *sqlx.DB
}

// NewPostgresBackend creates a new PostgreSQL-based backend connecting to the given DSN.
func NewPostgresBackend(dsn string) (*PostgresBackend, error) {
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if err := runPostgresMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}

	return &PostgresBackend{db: db}, nil
}

// NewPostgresStore creates a WorkflowStore backed by PostgreSQL at the given DSN.
func NewPostgresStore(dsn string) (*WorkflowStore, error) {
	backend, err := NewPostgresBackend(dsn)
	if err != nil {
		return nil, err
	}
	return NewWorkflowStore(backend), nil
}

// Save persists a workflow using INSERT ... ON CONFLICT upsert.
func (p *PostgresBackend) Save(w *Workflow) error {
	data, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("marshal workflow: %w", err)
	}

	const query = `
		INSERT INTO workflows (id, name, owner, state, idempotency_key, data, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT(id) DO UPDATE SET
			name            = EXCLUDED.name,
			owner           = EXCLUDED.owner,
			state           = EXCLUDED.state,
			idempotency_key = EXCLUDED.idempotency_key,
			data            = EXCLUDED.data,
			updated_at      = EXCLUDED.updated_at`

	_, err = p.db.Exec(query,
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
func (p *PostgresBackend) Load(id string) (*Workflow, bool) {
	var data []byte
	err := p.db.Get(&data, "SELECT data FROM workflows WHERE id = $1", id)
	if err != nil {
		return nil, false
	}

	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, false
	}
	return &w, true
}

// Delete removes a workflow by ID. Returns nil even if the workflow doesn't exist.
func (p *PostgresBackend) Delete(id string) error {
	_, err := p.db.Exec("DELETE FROM workflows WHERE id = $1", id)
	return err
}

// List returns workflows optionally filtered by state. Empty state returns all.
func (p *PostgresBackend) List(state WorkflowState) []*Workflow {
	return p.queryWorkflows(state, "")
}

// ListByOwner returns workflows owned by the given owner.
func (p *PostgresBackend) ListByOwner(owner string) []*Workflow {
	return p.queryWorkflows("", owner)
}

// FindByIdempotencyKey returns the first non-terminal workflow with the given key, or nil.
func (p *PostgresBackend) FindByIdempotencyKey(key string) *Workflow {
	var data []byte
	err := p.db.Get(&data,
		"SELECT data FROM workflows WHERE idempotency_key = $1 AND state NOT IN ($2, $3, $4) LIMIT 1",
		key, string(StateCompleted), string(StateFailed), string(StateCancelled),
	)
	if err != nil {
		return nil
	}

	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return nil
	}
	return &w
}

// Modify atomically loads a workflow, applies fn, and saves it back within a transaction.
func (p *PostgresBackend) Modify(id string, fn func(w *Workflow)) error {
	tx, err := p.db.Beginx()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var data []byte
	if err := tx.Get(&data, "SELECT data FROM workflows WHERE id = $1 FOR UPDATE", id); err != nil {
		return fmt.Errorf("workflow %s not found", id)
	}

	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return fmt.Errorf("unmarshal workflow %s: %w", id, err)
	}

	fn(&w)

	updated, err := json.Marshal(&w)
	if err != nil {
		return fmt.Errorf("marshal workflow %s: %w", id, err)
	}

	const updateQuery = `
		UPDATE workflows SET
			name            = $1,
			owner           = $2,
			state           = $3,
			idempotency_key = $4,
			data            = $5,
			updated_at      = $6
		WHERE id = $7`

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

// Close closes the underlying database connection pool.
func (p *PostgresBackend) Close() error {
	return p.db.Close()
}

// queryWorkflows builds a dynamic query with optional state and owner filters.
func (p *PostgresBackend) queryWorkflows(state WorkflowState, owner string) []*Workflow {
	query := "SELECT data FROM workflows WHERE 1=1"
	var args []any
	paramIdx := 1

	if state != "" {
		query += fmt.Sprintf(" AND state = $%d", paramIdx)
		args = append(args, string(state))
		paramIdx++
	}
	if owner != "" {
		query += fmt.Sprintf(" AND owner = $%d", paramIdx)
		args = append(args, owner)
		paramIdx++ //nolint:ineffassign
	}

	var rows [][]byte
	if err := p.db.Select(&rows, query, args...); err != nil {
		return nil
	}

	result := make([]*Workflow, 0, len(rows))
	for _, data := range rows {
		var w Workflow
		if err := json.Unmarshal(data, &w); err != nil {
			continue
		}
		result = append(result, &w)
	}
	return result
}

// runPostgresMigrations applies embedded SQL migration files.
func runPostgresMigrations(db *sqlx.DB) error {
	entries, err := postgresMigrations.ReadDir("migrate/postgres")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	for _, entry := range entries {
		sql, err := postgresMigrations.ReadFile("migrate/postgres/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if _, err := db.Exec(string(sql)); err != nil {
			return fmt.Errorf("exec %s: %w", entry.Name(), err)
		}
	}
	return nil
}
