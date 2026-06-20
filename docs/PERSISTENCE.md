# Persistence

go-workflow uses a pluggable `StoreBackend` interface for workflow storage. Three backends ship out of the box: JSON files, SQLite, and PostgreSQL. You can also implement your own.

## Backends

### JSON Files (default)

Stores each workflow as a JSON file on disk. Good for development and single-process deployments.

```go
store, err := store.NewFileStore("/path/to/workflows")
```

- In-memory map with `sync.RWMutex` for concurrent access
- Atomic writes via temp file + rename (no partial writes on crash)
- Loads all workflows from disk on startup
- No external dependencies

### SQLite

Pure Go SQLite via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (no CGo required). Good for tests and single-binary deployments.

```go
store, err := store.NewSQLiteStore("/path/to/workflows.db")
```

- WAL journal mode for better read concurrency
- `MaxOpenConns(1)` — SQLite doesn't support concurrent writers
- Transactional `Modify` (BEGIN → read → apply → write → COMMIT)
- Schema auto-migrated on startup

### PostgreSQL

JSONB-based storage via [pgx/v5](https://github.com/jackc/pgx) + [sqlx](https://github.com/jmoiron/sqlx). Production-grade for multi-process deployments.

```go
store, err := store.NewPostgresStore("postgres://user:pass@localhost:5432/go_workflow?sslmode=disable")
```

- `SELECT ... FOR UPDATE` in `Modify` for row-level locking
- JSONB `data` column stores full workflow snapshot
- Denormalized columns (`state`, `owner`, `idempotency_key`) for indexed filtering
- Partial unique index on `idempotency_key` — enforces uniqueness at DB level for active workflows
- Schema auto-migrated on startup via embedded SQL

## Schema

Both SQL backends use the same logical schema:

```sql
CREATE TABLE workflows (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'pending',
    idempotency_key TEXT,
    data            JSONB NOT NULL,       -- full workflow JSON snapshot
    created_at      BIGINT NOT NULL DEFAULT 0,
    updated_at      BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX idx_workflows_state ON workflows(state);
CREATE INDEX idx_workflows_owner ON workflows(owner);

-- PostgreSQL only: prevents duplicate active workflows with same key
CREATE UNIQUE INDEX idx_workflows_idempotency ON workflows(idempotency_key)
    WHERE idempotency_key IS NOT NULL
    AND state NOT IN ('completed', 'failed', 'cancelled');
```

The `data` column stores the entire `Workflow` struct as JSON. The denormalized columns (`state`, `owner`, `idempotency_key`) are extracted for efficient indexed queries.

## Architecture

```
WorkflowStore (wrapper)
├── clone on Save (entry)
├── clone on Load/List (exit)
└── delegates to StoreBackend
    ├── FileBackend      (store/file.go)
    ├── SQLiteBackend    (store/sqlite.go)
    └── PostgresBackend  (store/postgres.go)
```

`WorkflowStore` wraps any `StoreBackend` with clone-on-entry/exit semantics. This ensures callers always get independent copies — mutations to returned workflows never affect the store's internal state.

Backends handle persistence and concurrency. They do **not** clone — that's the wrapper's responsibility.

## StoreBackend Interface

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

### Method contracts

| Method | Behavior |
|--------|----------|
| `Save` | Upsert — creates or replaces a workflow by ID |
| `Load` | Returns `(nil, false)` if not found |
| `Delete` | No-op if workflow doesn't exist |
| `List` | Empty `state` returns all workflows |
| `ListByOwner` | Filters by `Owner` field |
| `FindByIdempotencyKey` | Returns first non-terminal (not completed/failed/cancelled) match, or nil |
| `Modify` | Atomic read-modify-write. Returns error if workflow not found |
| `Close` | Release resources (DB connections, file handles) |

## Custom Backends

Implement `StoreBackend` and wrap with `NewWorkflowStore`:

```go
type RedisBackend struct { /* ... */ }

func (r *RedisBackend) Save(w *workflow.Workflow) error { /* ... */ }
func (r *RedisBackend) Load(id string) (*workflow.Workflow, bool) { /* ... */ }
// ... implement all 8 methods

store := workflow.NewWorkflowStore(&RedisBackend{client: redisClient})
engine := workflow.NewEngine(store)
```

## Crash Recovery

Each `Modify` call persists a full workflow snapshot. On process crash:

1. The last completed step is preserved in storage
2. On restart, call `engine.RecoverAll(ctx)` to find workflows stuck in `StateRunning`
3. `RecoverAll` resets `StepRunning` steps back to `StepPending` and resumes execution

```go
engine := workflow.NewEngine(store)
recovered := engine.RecoverAll(context.Background())
// recovered = ["wf-abc", "wf-xyz"] — IDs of workflows that were resumed
```

This works with all backends. The snapshot-per-Modify approach means no separate write-ahead log is needed.

## Migrations

SQL migrations are embedded in the binary via `//go:embed` and applied automatically on backend construction. Migration files:

```
store/migrate/
├── postgres/
│   ├── 001_init.sql
│   └── 002_step_queue.sql
└── sqlite/
    └── 001_init.sql
```

To add a new migration, create `002_*.sql` in the appropriate directory. Migrations run sequentially by filename and use `IF NOT EXISTS` / `IF NOT EXISTS` guards for idempotency.

## Testing

A conformance test suite (`store/conformance_test.go`) runs 11 subtests against every backend:

- SaveLoad, LoadNotFound, Delete, DeleteNonexistent
- ListByState, ListByOwner, Modify, ModifyNotFound
- FindByIdempotencyKey, CloneIsolation, ConcurrentAccess

PostgreSQL tests require a `WORKFLOW_TEST_POSTGRES_DSN` environment variable and are skipped otherwise.
