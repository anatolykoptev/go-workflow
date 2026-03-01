# Pluggable Persistence Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Extract a `StoreBackend` interface from the current monolithic `WorkflowStore`, implement JSON file / PostgreSQL / SQLite backends, and add crash recovery.

**Architecture:** `WorkflowStore` becomes a thin wrapper that clones on entry/exit and delegates to a `StoreBackend` interface. Three backends implement raw persistence: `FileBackend` (current logic), `PostgresBackend` (sqlx+pgx, JSONB snapshots), `SQLiteBackend` (modernc/sqlite). A conformance test suite validates all backends identically.

**Tech Stack:** Go 1.26, sqlx, pgx/v5, modernc.org/sqlite, embed (migrations)

---

### Task 1: Extract StoreBackend interface and refactor WorkflowStore

**Files:**
- Modify: `store.go` — replace struct with interface + wrapper
- Modify: `testhelpers_test.go:67-75` — update `newTestStore` to use `NewFileStore`

**Step 1: Write the failing test**

Add to `store_test.go`:

```go
func TestNewFileStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(filepath.Join(dir, "workflows"))
	if err != nil {
		t.Fatal(err)
	}

	wf := NewWorkflow("wf1", "Test", "owner", nil)
	if err := store.Save(wf); err != nil {
		t.Fatal(err)
	}

	loaded, ok := store.Load("wf1")
	if !ok {
		t.Fatal("not found")
	}
	if loaded.Name != "Test" {
		t.Errorf("name = %q, want Test", loaded.Name)
	}

	// Verify Close works
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -run TestNewFileStore -v -count=1`
Expected: FAIL — `NewFileStore` undefined, `Close` undefined

**Step 3: Rewrite store.go — interface + wrapper**

Replace the entire `store.go` with:

```go
package workflow

// StoreBackend is the raw persistence layer.
// Implementations handle serialization, locking, and I/O.
// They do NOT clone — WorkflowStore handles that.
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

// WorkflowStore provides clone-on-read/write semantics around a StoreBackend.
// All public methods clone workflows to prevent callers from mutating stored state.
type WorkflowStore struct {
	backend StoreBackend
}

// NewWorkflowStore creates a store backed by the given StoreBackend.
func NewWorkflowStore(backend StoreBackend) *WorkflowStore {
	return &WorkflowStore{backend: backend}
}

// NewFileStore creates a WorkflowStore backed by JSON files in the given directory.
// This is the default backend and preserves v0.2.0 behavior.
func NewFileStore(dir string) (*WorkflowStore, error) {
	fb, err := NewFileBackend(dir)
	if err != nil {
		return nil, err
	}
	return NewWorkflowStore(fb), nil
}

func (s *WorkflowStore) Save(w *Workflow) error {
	return s.backend.Save(w.clone())
}

func (s *WorkflowStore) Load(id string) (*Workflow, bool) {
	w, ok := s.backend.Load(id)
	if !ok {
		return nil, false
	}
	return w.clone(), true
}

func (s *WorkflowStore) Delete(id string) error {
	return s.backend.Delete(id)
}

func (s *WorkflowStore) List(state WorkflowState) []*Workflow {
	results := s.backend.List(state)
	cloned := make([]*Workflow, len(results))
	for i, w := range results {
		cloned[i] = w.clone()
	}
	return cloned
}

func (s *WorkflowStore) ListByOwner(owner string) []*Workflow {
	results := s.backend.ListByOwner(owner)
	cloned := make([]*Workflow, len(results))
	for i, w := range results {
		cloned[i] = w.clone()
	}
	return cloned
}

func (s *WorkflowStore) FindByIdempotencyKey(key string) *Workflow {
	w := s.backend.FindByIdempotencyKey(key)
	if w == nil {
		return nil
	}
	return w.clone()
}

func (s *WorkflowStore) Modify(id string, fn func(w *Workflow)) error {
	return s.backend.Modify(id, fn)
}

// Close releases resources held by the backend (connections, file handles).
func (s *WorkflowStore) Close() error {
	return s.backend.Close()
}
```

**Step 4: Create store_file.go — extract FileBackend**

Move all current file-based logic into `store_file.go`:

```go
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileBackend stores workflows as individual JSON files in a directory.
// Thread-safe via sync.RWMutex. Atomic writes via temp file + rename.
type FileBackend struct {
	dir       string
	workflows map[string]*Workflow
	mu        sync.RWMutex
}

// NewFileBackend creates a file-based backend in the given directory.
func NewFileBackend(dir string) (*FileBackend, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workflow dir: %w", err)
	}
	fb := &FileBackend{
		dir:       dir,
		workflows: make(map[string]*Workflow),
	}
	if err := fb.loadAll(); err != nil {
		return nil, fmt.Errorf("load workflows: %w", err)
	}
	return fb, nil
}

func (f *FileBackend) Save(w *Workflow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workflows[w.ID] = w
	return f.writeToDisk(w)
}

func (f *FileBackend) Load(id string) (*Workflow, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	w, ok := f.workflows[id]
	if !ok {
		return nil, false
	}
	return w, true
}

func (f *FileBackend) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.workflows, id)
	path := filepath.Join(f.dir, id+".json")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (f *FileBackend) List(state WorkflowState) []*Workflow {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var result []*Workflow
	for _, w := range f.workflows {
		if state == "" || w.State == state {
			result = append(result, w)
		}
	}
	return result
}

func (f *FileBackend) ListByOwner(owner string) []*Workflow {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var result []*Workflow
	for _, w := range f.workflows {
		if w.Owner == owner {
			result = append(result, w)
		}
	}
	return result
}

func (f *FileBackend) FindByIdempotencyKey(key string) *Workflow {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, w := range f.workflows {
		if w.IdempotencyKey == key && !w.IsTerminal() {
			return w
		}
	}
	return nil
}

func (f *FileBackend) Modify(id string, fn func(w *Workflow)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workflows[id]
	if !ok {
		return fmt.Errorf("workflow %s not found", id)
	}
	fn(w)
	return f.writeToDisk(w)
}

func (f *FileBackend) Close() error { return nil }

func (f *FileBackend) writeToDisk(w *Workflow) error {
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(f.dir, w.ID+".json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (f *FileBackend) loadAll() error {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.dir, entry.Name()))
		if err != nil {
			continue
		}
		var w Workflow
		if err := json.Unmarshal(data, &w); err != nil {
			continue
		}
		f.workflows[w.ID] = &w
	}
	return nil
}
```

**Step 5: Update testhelpers_test.go**

Change `newTestStore`:

```go
func newTestStore(t *testing.T) *WorkflowStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileStore(filepath.Join(dir, "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
```

**Step 6: Update store_test.go — TestStorePersistence**

`TestStorePersistence` calls `NewWorkflowStore(wfDir)` directly. Update to `NewFileStore(wfDir)`:

```go
func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")

	store1, _ := NewFileStore(wfDir)
	wf := NewWorkflow("wf1", "Persistent", "", []Step{
		{ID: "s1", Kind: StepTool, State: StepCompleted},
	})
	_ = store1.Save(wf)

	store2, _ := NewFileStore(wfDir)
	loaded, ok := store2.Load("wf1")
	if !ok {
		t.Fatal("workflow not found after reload")
	}
	if loaded.Name != "Persistent" {
		t.Errorf("name = %q, want %q", loaded.Name, "Persistent")
	}
}
```

Same for `TestStoreAtomicWrite`:

```go
func TestStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, "workflows")
	store, _ := NewFileStore(wfDir)
	// ... rest unchanged
}
```

**Step 7: Run all tests to verify zero behavior change**

Run: `go test ./... -count=1`
Expected: all 106+ tests PASS

**Step 8: Run lint**

Run: `make lint`
Expected: 0 issues

**Step 9: Commit**

```bash
git add store.go store_file.go store_test.go testhelpers_test.go
git commit -m "refactor: extract StoreBackend interface, move file logic to FileBackend"
```

---

### Task 2: Conformance test suite

**Files:**
- Create: `store_conformance_test.go` — shared test runner for all backends
- Modify: `store_test.go` — wire FileBackend into conformance suite

**Step 1: Write conformance test runner**

Create `store_conformance_test.go`:

```go
package workflow

import (
	"sync"
	"testing"
)

// runConformanceTests exercises all StoreBackend contract requirements.
// Call this from each backend-specific test file.
func runConformanceTests(t *testing.T, name string, newStore func(t *testing.T) *WorkflowStore) {
	t.Run(name+"/SaveLoad", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		wf := NewWorkflow("wf1", "Test", "owner:1", []Step{
			{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepPending},
		})
		if err := store.Save(wf); err != nil {
			t.Fatal(err)
		}

		loaded, ok := store.Load("wf1")
		if !ok {
			t.Fatal("not found after save")
		}
		if loaded.Name != "Test" {
			t.Errorf("name = %q, want Test", loaded.Name)
		}
		if loaded.Owner != "owner:1" {
			t.Errorf("owner = %q, want owner:1", loaded.Owner)
		}
		if len(loaded.Steps) != 1 {
			t.Errorf("steps = %d, want 1", len(loaded.Steps))
		}
	})

	t.Run(name+"/LoadNotFound", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		_, ok := store.Load("nonexistent")
		if ok {
			t.Error("expected not found")
		}
	})

	t.Run(name+"/Delete", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		_ = store.Save(NewWorkflow("wf1", "Del", "", nil))
		if err := store.Delete("wf1"); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.Load("wf1"); ok {
			t.Error("still present after delete")
		}
	})

	t.Run(name+"/DeleteNonexistent", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		// Should not error
		if err := store.Delete("ghost"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run(name+"/ListByState", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		_ = store.Save(NewWorkflow("wf1", "A", "", nil))
		wf2 := NewWorkflow("wf2", "B", "", nil)
		wf2.State = StateRunning
		_ = store.Save(wf2)

		all := store.List("")
		if len(all) != 2 {
			t.Errorf("list all = %d, want 2", len(all))
		}
		running := store.List(StateRunning)
		if len(running) != 1 {
			t.Errorf("list running = %d, want 1", len(running))
		}
	})

	t.Run(name+"/ListByOwner", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		_ = store.Save(NewWorkflow("wf1", "A", "alice", nil))
		_ = store.Save(NewWorkflow("wf2", "B", "bob", nil))
		_ = store.Save(NewWorkflow("wf3", "C", "alice", nil))

		owned := store.ListByOwner("alice")
		if len(owned) != 2 {
			t.Errorf("owned = %d, want 2", len(owned))
		}
	})

	t.Run(name+"/Modify", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		_ = store.Save(NewWorkflow("wf1", "Mod", "", nil))
		err := store.Modify("wf1", func(w *Workflow) {
			w.State = StateRunning
		})
		if err != nil {
			t.Fatal(err)
		}

		loaded, _ := store.Load("wf1")
		if loaded.State != StateRunning {
			t.Errorf("state = %s, want running", loaded.State)
		}
	})

	t.Run(name+"/ModifyNotFound", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		err := store.Modify("ghost", func(w *Workflow) {})
		if err == nil {
			t.Error("expected error for nonexistent workflow")
		}
	})

	t.Run(name+"/FindByIdempotencyKey", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		wf := NewWorkflow("wf1", "Idemp", "", nil)
		wf.IdempotencyKey = "order-1"
		wf.State = StateRunning
		_ = store.Save(wf)

		found := store.FindByIdempotencyKey("order-1")
		if found == nil {
			t.Fatal("expected to find active workflow")
		}
		if found.ID != "wf1" {
			t.Errorf("id = %q, want wf1", found.ID)
		}

		// Terminal workflows should not be found
		_ = store.Modify("wf1", func(w *Workflow) {
			w.State = StateCompleted
		})
		if got := store.FindByIdempotencyKey("order-1"); got != nil {
			t.Error("expected nil for terminal workflow")
		}
	})

	t.Run(name+"/CloneIsolation", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		_ = store.Save(NewWorkflow("wf1", "Clone", "", nil))
		loaded, _ := store.Load("wf1")
		loaded.Name = "Mutated"

		reloaded, _ := store.Load("wf1")
		if reloaded.Name != "Clone" {
			t.Errorf("name = %q, want Clone (mutation leaked)", reloaded.Name)
		}
	})

	t.Run(name+"/ConcurrentAccess", func(t *testing.T) {
		store := newStore(t)
		defer store.Close()

		_ = store.Save(NewWorkflow("wf1", "Concurrent", "", nil))

		var wg sync.WaitGroup
		for i := range 10 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				_ = store.Modify("wf1", func(w *Workflow) {
					w.Name = "updated"
				})
				_, _ = store.Load("wf1")
			}(i)
		}
		wg.Wait()

		loaded, ok := store.Load("wf1")
		if !ok {
			t.Fatal("not found after concurrent access")
		}
		if loaded.Name != "updated" {
			t.Errorf("name = %q, want updated", loaded.Name)
		}
	})
}
```

**Step 2: Wire FileBackend into conformance suite**

Add to `store_test.go`:

```go
func TestFileBackend_Conformance(t *testing.T) {
	runConformanceTests(t, "FileBackend", func(t *testing.T) *WorkflowStore {
		t.Helper()
		dir := t.TempDir()
		store, err := NewFileStore(filepath.Join(dir, "workflows"))
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}
```

**Step 3: Run tests**

Run: `go test -run TestFileBackend_Conformance -v -count=1`
Expected: all subtests PASS

**Step 4: Commit**

```bash
git add store_conformance_test.go store_test.go
git commit -m "test: add store backend conformance test suite"
```

---

### Task 3: Add dependencies (sqlx, pgx, modernc/sqlite)

**Files:**
- Modify: `go.mod`

**Step 1: Add dependencies**

```bash
go get github.com/jmoiron/sqlx
go get github.com/jackc/pgx/v5/stdlib
go get modernc.org/sqlite
```

**Step 2: Verify build**

Run: `go build .`
Expected: success

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add sqlx, pgx, modernc/sqlite for pluggable persistence"
```

---

### Task 4: SQLite backend

SQLite before PostgreSQL — easier to test (no external DB), validates the interface works with real SQL.

**Files:**
- Create: `store_sqlite.go`
- Create: `migrate/sqlite/001_init.sql`
- Modify: `store_test.go` — add SQLite conformance test

**Step 1: Create migration**

Create `migrate/sqlite/001_init.sql`:

```sql
CREATE TABLE IF NOT EXISTS workflows (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'pending',
    idempotency_key TEXT,
    data            TEXT NOT NULL,
    created_at      INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_workflows_state ON workflows(state);
CREATE INDEX IF NOT EXISTS idx_workflows_owner ON workflows(owner);
```

**Step 2: Write the failing conformance test**

Add to `store_test.go`:

```go
func TestSQLiteBackend_Conformance(t *testing.T) {
	runConformanceTests(t, "SQLiteBackend", func(t *testing.T) *WorkflowStore {
		t.Helper()
		dbPath := filepath.Join(t.TempDir(), "test.db")
		store, err := NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}
```

**Step 3: Run test to verify it fails**

Run: `go test -run TestSQLiteBackend_Conformance -v -count=1`
Expected: FAIL — `NewSQLiteStore` undefined

**Step 4: Implement SQLiteBackend**

Create `store_sqlite.go`:

```go
package workflow

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed migrate/sqlite/*.sql
var sqliteMigrations embed.FS

// SQLiteBackend stores workflows in a SQLite database.
type SQLiteBackend struct {
	db *sqlx.DB
}

// NewSQLiteBackend creates a SQLite-backed store at the given path.
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

// NewSQLiteStore creates a WorkflowStore backed by SQLite.
func NewSQLiteStore(path string) (*WorkflowStore, error) {
	sb, err := NewSQLiteBackend(path)
	if err != nil {
		return nil, err
	}
	return NewWorkflowStore(sb), nil
}

func runSQLiteMigrations(db *sqlx.DB) error {
	data, err := sqliteMigrations.ReadFile("migrate/sqlite/001_init.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(string(data))
	return err
}

func (s *SQLiteBackend) Save(w *Workflow) error {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		INSERT INTO workflows (id, name, owner, state, idempotency_key, data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			owner = excluded.owner,
			state = excluded.state,
			idempotency_key = excluded.idempotency_key,
			data = excluded.data,
			updated_at = excluded.updated_at`,
		w.ID, w.Name, w.Owner, string(w.State), nilIfEmpty(w.IdempotencyKey),
		string(data), w.CreatedAt, w.UpdatedAt)
	return err
}

func (s *SQLiteBackend) Load(id string) (*Workflow, bool) {
	var data string
	err := s.db.QueryRow("SELECT data FROM workflows WHERE id = ?", id).Scan(&data)
	if err != nil {
		return nil, false
	}
	var w Workflow
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		return nil, false
	}
	return &w, true
}

func (s *SQLiteBackend) Delete(id string) error {
	_, err := s.db.Exec("DELETE FROM workflows WHERE id = ?", id)
	return err
}

func (s *SQLiteBackend) List(state WorkflowState) []*Workflow {
	return s.queryWorkflows(state, "")
}

func (s *SQLiteBackend) ListByOwner(owner string) []*Workflow {
	return s.queryWorkflows("", owner)
}

func (s *SQLiteBackend) queryWorkflows(state WorkflowState, owner string) []*Workflow {
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

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []*Workflow
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var w Workflow
		if err := json.Unmarshal([]byte(data), &w); err != nil {
			continue
		}
		result = append(result, &w)
	}
	return result
}

func (s *SQLiteBackend) FindByIdempotencyKey(key string) *Workflow {
	var data string
	err := s.db.QueryRow(`
		SELECT data FROM workflows
		WHERE idempotency_key = ?
		AND state NOT IN ('completed', 'failed', 'cancelled')
		LIMIT 1`, key).Scan(&data)
	if err != nil {
		return nil
	}
	var w Workflow
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		return nil
	}
	return &w
}

func (s *SQLiteBackend) Modify(id string, fn func(w *Workflow)) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var data string
	err = tx.QueryRow("SELECT data FROM workflows WHERE id = ?", id).Scan(&data)
	if err != nil {
		return fmt.Errorf("workflow %s not found", id)
	}

	var w Workflow
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		return err
	}

	fn(&w)

	newData, err := json.Marshal(&w)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		UPDATE workflows SET
			name = ?, owner = ?, state = ?, idempotency_key = ?,
			data = ?, updated_at = ?
		WHERE id = ?`,
		w.Name, w.Owner, string(w.State), nilIfEmpty(w.IdempotencyKey),
		string(newData), w.UpdatedAt, w.ID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *SQLiteBackend) Close() error {
	return s.db.Close()
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
```

**Step 5: Run conformance tests**

Run: `go test -run "TestSQLiteBackend_Conformance|TestFileBackend_Conformance" -v -count=1`
Expected: all subtests PASS for both backends

**Step 6: Run full suite + lint**

Run: `go test ./... -count=1 && make lint`
Expected: all pass, 0 issues

**Step 7: Commit**

```bash
git add store_sqlite.go migrate/ store_test.go
git commit -m "feat: add SQLite store backend with conformance tests"
```

---

### Task 5: PostgreSQL backend

**Files:**
- Create: `store_postgres.go`
- Create: `migrate/postgres/001_init.sql`
- Modify: `store_test.go` — add PostgreSQL conformance test (skipped without DB)

**Step 1: Create migration**

Create `migrate/postgres/001_init.sql`:

```sql
CREATE TABLE IF NOT EXISTS workflows (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'pending',
    idempotency_key TEXT,
    data            JSONB NOT NULL,
    created_at      BIGINT NOT NULL DEFAULT 0,
    updated_at      BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_workflows_state ON workflows(state);
CREATE INDEX IF NOT EXISTS idx_workflows_owner ON workflows(owner);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflows_idempotency ON workflows(idempotency_key)
    WHERE idempotency_key IS NOT NULL
    AND state NOT IN ('completed', 'failed', 'cancelled');
```

**Step 2: Write the failing conformance test**

Add to `store_test.go`:

```go
func TestPostgresBackend_Conformance(t *testing.T) {
	dsn := os.Getenv("WORKFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("WORKFLOW_TEST_POSTGRES_DSN not set")
	}

	runConformanceTests(t, "PostgresBackend", func(t *testing.T) *WorkflowStore {
		t.Helper()
		store, err := NewPostgresStore(dsn)
		if err != nil {
			t.Fatal(err)
		}
		// Clean table between subtests
		store.backend.(*PostgresBackend).db.Exec("DELETE FROM workflows")
		return store
	})
}
```

**Step 3: Run test to verify it fails**

Run: `go test -run TestPostgresBackend_Conformance -v -count=1`
Expected: SKIP (no DSN) or FAIL (`NewPostgresStore` undefined)

**Step 4: Implement PostgresBackend**

Create `store_postgres.go`:

```go
package workflow

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrate/postgres/*.sql
var postgresMigrations embed.FS

// PostgresBackend stores workflows in a PostgreSQL database.
type PostgresBackend struct {
	db *sqlx.DB
}

// NewPostgresBackend creates a PostgreSQL-backed store.
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

// NewPostgresStore creates a WorkflowStore backed by PostgreSQL.
func NewPostgresStore(dsn string) (*WorkflowStore, error) {
	pb, err := NewPostgresBackend(dsn)
	if err != nil {
		return nil, err
	}
	return NewWorkflowStore(pb), nil
}

func runPostgresMigrations(db *sqlx.DB) error {
	data, err := postgresMigrations.ReadFile("migrate/postgres/001_init.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(string(data))
	return err
}

func (p *PostgresBackend) Save(w *Workflow) error {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = p.db.Exec(`
		INSERT INTO workflows (id, name, owner, state, idempotency_key, data, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT(id) DO UPDATE SET
			name = EXCLUDED.name,
			owner = EXCLUDED.owner,
			state = EXCLUDED.state,
			idempotency_key = EXCLUDED.idempotency_key,
			data = EXCLUDED.data,
			updated_at = EXCLUDED.updated_at`,
		w.ID, w.Name, w.Owner, string(w.State), nilIfEmpty(w.IdempotencyKey),
		data, w.CreatedAt, w.UpdatedAt)
	return err
}

func (p *PostgresBackend) Load(id string) (*Workflow, bool) {
	var data []byte
	err := p.db.QueryRow("SELECT data FROM workflows WHERE id = $1", id).Scan(&data)
	if err != nil {
		return nil, false
	}
	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, false
	}
	return &w, true
}

func (p *PostgresBackend) Delete(id string) error {
	_, err := p.db.Exec("DELETE FROM workflows WHERE id = $1", id)
	return err
}

func (p *PostgresBackend) List(state WorkflowState) []*Workflow {
	return p.queryWorkflows(state, "")
}

func (p *PostgresBackend) ListByOwner(owner string) []*Workflow {
	return p.queryWorkflows("", owner)
}

func (p *PostgresBackend) queryWorkflows(state WorkflowState, owner string) []*Workflow {
	query := "SELECT data FROM workflows WHERE true"
	var args []any
	argN := 1
	if state != "" {
		query += fmt.Sprintf(" AND state = $%d", argN)
		args = append(args, string(state))
		argN++
	}
	if owner != "" {
		query += fmt.Sprintf(" AND owner = $%d", argN)
		args = append(args, owner)
	}

	rows, err := p.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var result []*Workflow
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var w Workflow
		if err := json.Unmarshal(data, &w); err != nil {
			continue
		}
		result = append(result, &w)
	}
	return result
}

func (p *PostgresBackend) FindByIdempotencyKey(key string) *Workflow {
	var data []byte
	err := p.db.QueryRow(`
		SELECT data FROM workflows
		WHERE idempotency_key = $1
		AND state NOT IN ('completed', 'failed', 'cancelled')
		LIMIT 1`, key).Scan(&data)
	if err != nil {
		return nil
	}
	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return nil
	}
	return &w
}

func (p *PostgresBackend) Modify(id string, fn func(w *Workflow)) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var data []byte
	err = tx.QueryRow("SELECT data FROM workflows WHERE id = $1 FOR UPDATE", id).Scan(&data)
	if err != nil {
		return fmt.Errorf("workflow %s not found", id)
	}

	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}

	fn(&w)

	newData, err := json.Marshal(&w)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		UPDATE workflows SET
			name = $1, owner = $2, state = $3, idempotency_key = $4,
			data = $5, updated_at = $6
		WHERE id = $7`,
		w.Name, w.Owner, string(w.State), nilIfEmpty(w.IdempotencyKey),
		newData, w.UpdatedAt, w.ID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (p *PostgresBackend) Close() error {
	return p.db.Close()
}
```

**Step 5: Run conformance tests with PostgreSQL**

```bash
# Create test DB (one-time)
docker exec -it krolik-server-postgres-1 psql -U postgres -c "CREATE DATABASE go_workflow_test"

# Run with DSN
WORKFLOW_TEST_POSTGRES_DSN="postgres://postgres:password@localhost:5432/go_workflow_test?sslmode=disable" \
  go test -run TestPostgresBackend_Conformance -v -count=1
```

Expected: all subtests PASS

**Step 6: Run full suite + lint**

Run: `go test ./... -count=1 && make lint`
Expected: all pass (postgres test skipped without DSN), 0 lint issues

**Step 7: Commit**

```bash
git add store_postgres.go migrate/postgres/
git commit -m "feat: add PostgreSQL store backend with conformance tests"
```

---

### Task 6: RecoverAll for crash recovery

**Files:**
- Modify: `engine_lifecycle.go` — add `RecoverAll` method
- Create/modify test file for `RecoverAll`

**Step 1: Write the failing test**

Add to `lifecycle_test.go`:

```go
func TestRecoverAll(t *testing.T) {
	runner := &mockToolRunner{results: map[string]string{"echo": "ok"}}
	engine, store := newTestEngine(t, runner)

	// Simulate crash: workflow stuck in running, step stuck in running
	wf := NewWorkflow("wf1", "Crashed", "owner", []Step{
		{ID: "s1", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepCompleted},
		{ID: "s2", Kind: StepTool, Config: map[string]any{"tool": "echo"}, State: StepRunning,
			DependsOn: []string{"s1"}},
	})
	wf.State = StateRunning
	_ = store.Save(wf)

	// Completed workflow should be untouched
	wf2 := NewWorkflow("wf2", "Done", "owner", nil)
	wf2.State = StateCompleted
	_ = store.Save(wf2)

	recovered := engine.RecoverAll(context.Background())
	if len(recovered) != 1 || recovered[0] != "wf1" {
		t.Errorf("recovered = %v, want [wf1]", recovered)
	}

	// Wait a bit for async execution
	time.Sleep(100 * time.Millisecond)

	loaded, _ := store.Load("wf1")
	if loaded.State != StateCompleted {
		t.Errorf("wf1 state = %s, want completed", loaded.State)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -run TestRecoverAll -v -count=1`
Expected: FAIL — `RecoverAll` undefined

**Step 3: Implement RecoverAll**

Add to `engine_lifecycle.go`:

```go
// RecoverAll finds workflows stuck in StateRunning at startup (sign of a crash)
// and resumes them. Running steps are reset to pending. Returns recovered IDs.
func (e *Engine) RecoverAll(ctx context.Context) []string {
	running := e.store.List(StateRunning)
	var recovered []string
	for _, w := range running {
		_ = e.store.Modify(w.ID, func(w *Workflow) {
			for i := range w.Steps {
				if w.Steps[i].State == StepRunning {
					w.Steps[i].State = StepPending
					w.Steps[i].StartedAt = 0
					e.log().Info("reset crashed step",
						"component", "workflow",
						"workflow", w.ID,
						"step", w.Steps[i].ID,
					)
				}
			}
			w.UpdatedAt = time.Now().UnixMilli()
		})
		recovered = append(recovered, w.ID)
		e.log().Info("recovered after crash",
			"component", "workflow",
			"workflow", w.ID,
		)
		go func(id string) {
			if err := e.RunToCompletion(ctx, id); err != nil {
				e.log().Error("recovery execution failed",
					"component", "workflow",
					"workflow", id,
					"error", err.Error(),
				)
			}
			e.notifyCompletion(id)
		}(w.ID)
	}
	return recovered
}
```

**Step 4: Run test**

Run: `go test -run TestRecoverAll -v -count=1`
Expected: PASS

**Step 5: Run full suite + lint**

Run: `go test ./... -count=1 && make lint`
Expected: all pass, 0 issues

**Step 6: Commit**

```bash
git add engine_lifecycle.go lifecycle_test.go
git commit -m "feat: add RecoverAll for crash recovery"
```

---

### Task 7: Update ROADMAP and tag v0.3.0

**Files:**
- Modify: `docs/ROADMAP.md` — check off v0.3.0 items

**Step 1: Update ROADMAP**

Mark all v0.3.0 items as `[x]`.

**Step 2: Final verification**

```bash
go test ./... -v -count=1
make lint
go build .
```

**Step 3: Commit, tag, push**

```bash
git add docs/ROADMAP.md
git commit -m "docs: mark v0.3.0 roadmap items complete"
git tag v0.3.0
git push origin main v0.3.0
```

**Step 4: Update Vaelor**

```bash
cd ~/src/vaelor
# Change NewWorkflowStore(wfDir) → NewFileStore(wfDir) in init_workflow.go
# (NewWorkflowStore now takes StoreBackend, not string)
go get github.com/anatolykoptev/go-workflow@v0.3.0
go build ./...
go test ./... -count=1
git add go.mod go.sum pkg/agent/init_workflow.go
git commit -m "Upgrade go-workflow to v0.3.0 (pluggable persistence)"
git push origin main
```
