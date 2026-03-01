# v0.3.0 — Pluggable Persistence Design

## Context

go-workflow v0.2.0 uses a single `WorkflowStore` backed by JSON files. For production use, we need PostgreSQL for multi-process access and transactional safety, and SQLite for tests and single-binary deployments. The store must be pluggable without breaking the existing API.

## Decisions

- **3 backends**: JSON file (default), PostgreSQL, SQLite
- **PostgreSQL**: separate DB `go_workflow` in existing postgres instance
- **Checkpointing**: snapshot per Modify (full workflow JSON on each mutation)
- **SQL drivers**: sqlx + pgx (postgres) + modernc.org/sqlite (pure Go, no CGo)
- **Interface approach**: Thin StoreBackend interface, WorkflowStore as clone wrapper

## Architecture

### StoreBackend Interface

```go
type StoreBackend interface {
    Save(w *Workflow) error
    Load(id string) (*Workflow, bool)
    Delete(id string) error
    List(state WorkflowState) []*Workflow
    ListByOwner(owner string) []*Workflow
    FindByIdempotencyKey(key string) *Workflow
    Modify(id string, fn func(w *Workflow)) error
    Close() error
}
```

Implementations handle serialization, locking, and I/O. They do NOT clone — WorkflowStore handles that.

### WorkflowStore Wrapper

```go
type WorkflowStore struct {
    backend StoreBackend
}

func NewWorkflowStore(backend StoreBackend) *WorkflowStore
func NewFileStore(dir string) (*WorkflowStore, error)    // convenience: JSON file backend
func NewPostgresStore(dsn string) (*WorkflowStore, error) // convenience: PostgreSQL backend
func NewSQLiteStore(path string) (*WorkflowStore, error)  // convenience: SQLite backend
```

Public methods remain identical. Save clones on entry, Load/List clone on exit. Modify delegates directly — backend ensures atomicity.

### JSON File Backend (store_file.go)

Current `store.go` logic moves here unchanged:
- In-memory `map[string]*Workflow` + `sync.RWMutex`
- Atomic write via temp file + rename
- `loadAll()` on construction
- `Close()` is a no-op

### PostgreSQL Backend (store_postgres.go)

**Database**: `go_workflow` (separate from application DBs)

**Schema**:

```sql
CREATE TABLE workflows (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL,
    state           TEXT NOT NULL,
    idempotency_key TEXT,
    data            JSONB NOT NULL,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
);

CREATE INDEX idx_workflows_state ON workflows(state);
CREATE INDEX idx_workflows_owner ON workflows(owner);
CREATE UNIQUE INDEX idx_workflows_idempotency ON workflows(idempotency_key)
    WHERE idempotency_key IS NOT NULL
    AND state NOT IN ('completed', 'failed', 'cancelled');
```

**Key patterns**:
- `data` column stores full Workflow JSON snapshot (same as file backend)
- Denormalized columns (state, owner, idempotency_key) for indexed filtering
- Partial unique index on idempotency_key — DB-level uniqueness for active workflows
- `Modify`: `SELECT ... FOR UPDATE` + apply fn + `UPDATE` in single transaction
- Migrations embedded via `embed.FS`, applied in `NewPostgresBackend()`

**Driver stack**: `sqlx` + `pgx/v5/stdlib` adapter

### SQLite Backend (store_sqlite.go)

Same schema adapted for SQLite:
- `modernc.org/sqlite` (pure Go, no CGo)
- No partial unique index — use application-level check in `Save`
- `Modify`: `BEGIN EXCLUSIVE` + read + apply fn + write + `COMMIT`
- `Close()` closes the database connection

### Step Checkpointing

Already works with snapshot-per-Modify architecture. Each completed step is persisted immediately. On crash recovery:

1. Backend loads last snapshot (all completed steps intact)
2. `ResumeAll()` finds running workflows, resets `StepRunning` → `StepPending`
3. Engine continues from first pending step

Addition: `RecoverAll()` method on Engine — finds workflows stuck in `StateRunning` at startup (sign of crash) and resets them via the same mechanism as `ResumeAll`.

## File Structure

```
store.go           — StoreBackend interface + WorkflowStore wrapper
store_file.go      — FileBackend (current logic, extracted)
store_postgres.go  — PostgresBackend
store_sqlite.go    — SQLiteBackend
store_test.go      — shared conformance tests (table-driven, all backends)
migrate/           — embedded SQL migrations for postgres and sqlite
```

## Migration Path

1. Extract interface + FileBackend (zero behavior change)
2. Add PostgresBackend + SQLiteBackend
3. Vaelor changes one line: `NewWorkflowStore(dir)` → `NewFileStore(dir)`
4. Later: Vaelor can switch to `NewPostgresStore(dsn)` when ready

## Testing

Conformance test suite runs the same test cases against all 3 backends:
- Save/Load/Delete round-trip
- List by state, ListByOwner
- Modify atomicity
- FindByIdempotencyKey (active vs terminal)
- Concurrent access (goroutine safety)
- Crash recovery simulation (kill mid-Modify, verify last good state)
