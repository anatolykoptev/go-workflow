# go-workflow Architecture Refactor Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix the 5 issues from the architecture audit — eliminate flat package problem, god file, global state, and missing docs — raising the score from 6/10 to 9/10.

**Architecture:** Extract store backends into `store/` subpackage to decouple heavy deps (pgx, sqlite) from core. Split god-file executors.go into per-executor files. Replace GlobalMetrics singleton with DI-injected `*Metrics`. Add ADR directory. Defer engine/ subpackage extraction to v1.0 (breaking API change not justified at v0.6).

**Tech Stack:** Go 1.26, pgx/v5, sqlx, modernc.org/sqlite, embed

---

## Decision: Why engine/ extraction is deferred

The audit suggests extracting `engine/` as a subpackage (priority #4, "High complexity"). This would:
- Break ALL existing consumers (`workflow.NewEngine` → `engine.New`)
- Require re-exporting types or circular import workarounds
- Give marginal benefit — engine files are already well-separated by name (`engine_*.go`)

**Verdict:** Defer to v1.0 where a breaking API change is planned anyway. The file-level separation already provides good SoC.

---

## Task 1: Extract store backends into `store/` subpackage

**Why:** Consumers who only need core types (`Workflow`, `Step`) currently pull in pgx + sqlite transitively. Extracting backends isolates heavy dependencies.

**Files:**
- Move: `store_file.go` → `store/file.go`
- Move: `store_sqlite.go` → `store/sqlite.go`
- Move: `store_postgres.go` → `store/postgres.go`
- Move: `migrate/` → `store/migrate/`
- Move: `store_conformance_test.go` → `store/conformance_test.go`
- Move: `store_test.go` → `store/store_test.go`
- Keep: `store.go` in root (contains `StoreBackend` interface + `WorkflowStore` — domain layer)
- Modify: `engine.go`, `engine_test.go`, `testhelpers_test.go` — update store creation calls

### Step 1: Create `store/` directory and move backend files

```bash
mkdir -p store
```

Move files, changing `package workflow` → `package store`:
- `store_file.go` → `store/file.go`
- `store_sqlite.go` → `store/sqlite.go`
- `store_postgres.go` → `store/postgres.go`
- `migrate/` → `store/migrate/`

### Step 2: Update package declaration and imports in moved files

Each moved file changes:
```go
// Before
package workflow

// After
package store

import "github.com/anatolykoptev/go-workflow" // for Workflow, StoreBackend, etc.
```

The `StoreBackend` interface stays in root package. Backend structs implement it from the `store` subpackage.

Key type references to update:
- `*Workflow` → `*workflow.Workflow`
- `WorkflowState` → `workflow.WorkflowState`
- `StoreBackend` → `workflow.StoreBackend`

### Step 3: Update embed directives

```go
// store/postgres.go
//go:embed migrate/postgres/*.sql
var postgresMigrations embed.FS

// store/sqlite.go
//go:embed migrate/sqlite/*.sql
var sqliteMigrations embed.FS
```

Paths stay relative to the new `store/` directory — embeddings are relative to the file's package dir.

### Step 4: Update convenience constructors in root store.go

```go
// store.go — remove NewFileStore, add a note pointing to store package
// Consumers now use:
//   import "github.com/anatolykoptev/go-workflow/store"
//   backend := store.NewFileBackend(dir)
//   ws := workflow.NewWorkflowStore(backend)
```

Remove `NewFileStore` from `store.go` (it was a convenience wrapper). If backward compat is needed, keep it as a deprecated wrapper that imports `store`.

Actually — since `store.go` is in root and can't import its own subpackage without creating a cycle, we must remove `NewFileStore` from root. This is the one intentional API break.

### Step 5: Move conformance tests

`store_conformance_test.go` → `store/conformance_test.go` (package store)

The conformance tests call `NewFileBackend`, `NewSQLiteBackend`, `NewPostgresBackend` — all now local to `store` package.

`store_test.go` tests `WorkflowStore` wrapping behavior → stays in root but needs updated imports:
```go
import "github.com/anatolykoptev/go-workflow/store"
// fb := store.NewFileBackend(dir)
```

Wait — `store_test.go` is `package workflow` and tests `WorkflowStore`. It can import `store` subpackage.

### Step 6: Update test helpers

`testhelpers_test.go` likely creates `FileBackend` for tests. Update:
```go
import workflowstore "github.com/anatolykoptev/go-workflow/store"
// fb, _ := workflowstore.NewFileBackend(t.TempDir())
```

### Step 7: Run tests and fix compilation

```bash
cd /home/krolik/src/go-workflow && go build ./...
cd /home/krolik/src/go-workflow && go test ./... -count=1 -race
```

Fix any remaining import issues.

### Step 8: Commit

```bash
git add store/ store.go store_test.go testhelpers_test.go engine_test.go
git rm store_file.go store_sqlite.go store_postgres.go store_conformance_test.go
git rm -r migrate/
git commit -m "refactor: extract store backends into store/ subpackage

Moves FileBackend, SQLiteBackend, PostgresBackend into store/ package.
Consumers who only need core types no longer pull pgx/sqlite transitively.
StoreBackend interface stays in root package (domain layer).

Breaking change: NewFileStore removed from root. Use:
  store.NewFileBackend(dir) + workflow.NewWorkflowStore(backend)"
```

---

## Task 2: Split executors.go into per-executor files

**Why:** `executors.go` (625 LOC) contains 9 executor types + utilities. SRP violation. Already partially done — `executor_foreach.go`, `executor_branchall.go`, `executor_suspend.go` exist as separate files. Finish the pattern.

**Files:**
- Split: `executors.go` → 7 files (same package, no import changes)
- Create: `executor_tool.go` (ToolExecutor + ToolRunner interface)
- Create: `executor_llm.go` (LLMExecutor + LLMProvider usage)
- Create: `executor_message.go` (MessageExecutor)
- Create: `executor_condition.go` (ConditionExecutor + isEmptyValue)
- Create: `executor_approval.go` (ApprovalExecutor + errApprovalRequired + NoopExecutor)
- Create: `executor_subworkflow.go` (SubWorkflowExecutor + SubWorkflowRunner interface)
- Create: `executor_agent.go` (AgentExecutor + AgentRunner + AgentRunOpts)
- Create: `executor_a2a.go` (A2AExecutor + A2ACaller interface)
- Keep: `executors.go` → rename to `resolve.go` (only resolveRef, resolvePromptRefs, ResolveRefs, resolveEnvVars, ParseOwner, envVarRegex — shared utilities)

### Step 1: Create executor files by extracting from executors.go

Each file gets the relevant struct + constructor + Execute method. All stay `package workflow`.

**executor_tool.go** (lines 13-54):
```go
package workflow
// StepExecutor interface, ToolRunner interface, ToolExecutor struct + Execute
```

**executor_llm.go** (lines 56-129):
```go
package workflow
// SkillResolver interface, LLMExecutor struct + Execute, SetSkills
```

**executor_message.go** (lines 131-172):
```go
package workflow
// MessageExecutor struct + Execute
```

**executor_condition.go** (lines 174-237):
```go
package workflow
// ConditionExecutor struct + Execute, isEmptyValue
```

**executor_approval.go** (lines 239-262):
```go
package workflow
// ApprovalExecutor, errApprovalRequired, NoopExecutor
```

**executor_subworkflow.go** (lines 264-324):
```go
package workflow
// SubWorkflowRunner interface, SubWorkflowExecutor struct + Execute
```

**executor_agent.go** (lines 326-400):
```go
package workflow
// AgentRunOpts, AgentRunner interface, AgentExecutor struct + Execute
```

**executor_a2a.go** (lines 402-447):
```go
package workflow
// A2ACaller interface, A2AExecutor struct + Execute
```

### Step 2: Rename executors.go → resolve.go

Keep only shared utilities (lines 449-626):
- `TransformExecutor` struct + Execute (stays here or gets `executor_transform.go`)
- `resolveRef`, `resolvePromptRefs`, `ResolveRefs`, `resolveEnvVars`, `envVarRegex`
- `ParseOwner`

Better split: `executor_transform.go` + `resolve.go` (only resolve* + ParseOwner).

### Step 3: Move StepExecutor interface to interfaces.go

`StepExecutor` interface is currently in `executors.go:14-16`. It's a port — belongs in `interfaces.go` alongside `MessagePublisher` and `LLMProvider`.

```go
// interfaces.go — add:
// StepExecutor runs a single step within a workflow.
type StepExecutor interface {
    Execute(ctx context.Context, step *Step, wf *Workflow) error
}
```

### Step 4: Verify no duplicate declarations

```bash
cd /home/krolik/src/go-workflow && go build ./...
```

### Step 5: Run tests

```bash
cd /home/krolik/src/go-workflow && go test ./... -count=1 -race
```

### Step 6: Commit

```bash
git add executor_*.go resolve.go interfaces.go
git rm executors.go
git commit -m "refactor: split executors.go into per-executor files

Each executor type now has its own file following the existing pattern
(executor_foreach.go, executor_branchall.go, executor_suspend.go).
Shared resolve* utilities moved to resolve.go.
StepExecutor interface moved to interfaces.go (port layer)."
```

---

## Task 3: Replace GlobalMetrics with DI-injected *Metrics

**Why:** `GlobalMetrics` is mutable global state causing test race issues (forced `t.Parallel()` removal). The fix: pass `*Metrics` through DI, keep `GlobalMetrics` as deprecated default.

**Files:**
- Modify: `metrics.go` — deprecate `GlobalMetrics`, add `NewMetrics()`
- Modify: `engine.go` — add `metrics *Metrics` field + `WithMetrics()` option
- Modify: `engine_lifecycle.go` — replace `GlobalMetrics.` → `e.metrics.`
- Modify: `engine_step.go` — replace `GlobalMetrics.` → `e.metrics.`
- Modify: `engine_watchdog.go` — replace `GlobalMetrics.` → `e.metrics.`
- Modify: `executors.go` (or new executor files) — executors that record metrics need access
- Modify: `scheduler.go` — replace `GlobalMetrics.` → `s.metrics.`
- Modify: `triggers.go` — replace `GlobalMetrics.` → `ts.metrics.`
- Modify: `metrics_export.go` — `PrometheusHandler` takes `*Metrics` parameter
- Modify: all `*_test.go` that call `GlobalMetrics.Reset()` — use per-test `NewMetrics()`

### Step 1: Add `NewMetrics()` constructor, deprecate GlobalMetrics

```go
// metrics.go

// NewMetrics creates a fresh Metrics instance (no global state).
func NewMetrics() *Metrics {
    return &Metrics{}
}

// Deprecated: GlobalMetrics is a package-level singleton. Use NewMetrics() + WithMetrics() instead.
var GlobalMetrics = NewMetrics()
```

### Step 2: Add metrics field to Engine

```go
// engine.go
type Engine struct {
    // ... existing fields
    metrics *Metrics
}

func WithMetrics(m *Metrics) EngineOption {
    return func(e *Engine) { e.metrics = m }
}

// In NewEngine constructor:
func NewEngine(store *WorkflowStore, opts ...EngineOption) *Engine {
    e := &Engine{
        // ...
        metrics: GlobalMetrics, // backward compat default
    }
    // ...
}
```

### Step 3: Add metrics field to executors that record metrics

`AgentExecutor`, `A2AExecutor`, `LLMExecutor` call `GlobalMetrics` directly. Options:

**Option A (simple):** Pass `*Metrics` to executor constructors:
```go
func NewAgentExecutor(runner AgentRunner, metrics *Metrics) *AgentExecutor {
    return &AgentExecutor{runner: runner, metrics: metrics}
}
```

**Option B (minimal change):** Engine passes its metrics when registering executors in `NewEngine` and `With*` options.

Go with Option A — explicit DI, each executor owns its metrics reference.

### Step 4: Replace all GlobalMetrics references in engine_*.go

In `engine.go`, `engine_lifecycle.go`, `engine_step.go`, `engine_watchdog.go`:
```go
// Before
GlobalMetrics.WorkflowsCreated.Add(1)
// After
e.metrics.WorkflowsCreated.Add(1)
```

### Step 5: Replace GlobalMetrics in scheduler.go and triggers.go

Add `metrics *Metrics` field to `Scheduler` and `TriggerService`:
```go
type Scheduler struct {
    // ...
    metrics *Metrics
}
```

Pass via constructor or option. Default to `GlobalMetrics` for backward compat.

### Step 6: Update PrometheusHandler

```go
// Before
func PrometheusHandler() http.Handler {

// After
func PrometheusHandler(m *Metrics) http.Handler {
```

Callers pass their metrics instance.

### Step 7: Update tests — per-test Metrics

```go
// Before
func TestSomething(t *testing.T) {
    GlobalMetrics.Reset()
    // ...
    if GlobalMetrics.WorkflowsCreated.Load() != 1 { ... }
}

// After
func TestSomething(t *testing.T) {
    t.Parallel() // now safe!
    m := NewMetrics()
    store := NewWorkflowStore(...)
    engine := NewEngine(store, WithMetrics(m))
    // ...
    if m.WorkflowsCreated.Load() != 1 { ... }
}
```

### Step 8: Run tests with -race

```bash
cd /home/krolik/src/go-workflow && go test ./... -count=1 -race
```

All tests should pass with `t.Parallel()` restored.

### Step 9: Commit

```bash
git commit -m "refactor: replace GlobalMetrics singleton with DI-injected *Metrics

Engine, Scheduler, TriggerService, and executors now receive *Metrics
through constructors/options. Tests use per-test NewMetrics() and can
safely run with t.Parallel(). GlobalMetrics kept as deprecated default
for backward compatibility."
```

---

## Task 4: Add ADR directory with initial records

**Why:** Audit §3.5 — no Architecture Decision Records documenting key design choices.

**Files:**
- Create: `docs/adr/0001-flat-package-design.md`
- Create: `docs/adr/0002-pluggable-storage-backends.md`
- Create: `docs/adr/0003-global-metrics-to-di.md`
- Create: `docs/adr/0004-store-subpackage-extraction.md`

### Step 1: Create ADR directory

```bash
mkdir -p docs/adr
```

### Step 2: Write initial ADRs

Each ADR follows the standard format:
```markdown
# ADR-NNNN: Title

**Status:** Accepted | Superseded
**Date:** YYYY-MM-DD
**Context:** Why this decision was needed
**Decision:** What was decided
**Consequences:** Trade-offs and implications
```

Write 4 ADRs documenting:
1. Original flat package choice (accepted, partially superseded by store/ extraction)
2. Pluggable storage via StoreBackend interface (accepted)
3. GlobalMetrics → DI migration (accepted, supersedes implicit global)
4. Store subpackage extraction (accepted)

### Step 3: Commit

```bash
git add docs/adr/
git commit -m "docs: add Architecture Decision Records (ADR)

Initial ADRs documenting flat package design, pluggable storage,
metrics DI migration, and store subpackage extraction."
```

---

## Execution Order & Dependencies

```
Task 2 (split executors)  ──→  Task 3 (metrics DI) ──→  Task 4 (ADR)
                                     ↑
Task 1 (store/ subpackage)  ─────────┘
```

- **Task 1 and Task 2** are independent — can run in parallel
- **Task 3** depends on Task 2 (executor files must exist before adding metrics field)
- **Task 3** optionally depends on Task 1 (store tests need updated metrics too)
- **Task 4** runs last (documents all decisions made)

## Expected Result

| Metric | Before | After |
|--------|--------|-------|
| Flat package files | 47 | ~20 (root) + 5 (store/) |
| executors.go LOC | 625 | 0 (split into 10 files × ~60 LOC) |
| GlobalMetrics refs | 63 | 0 production, deprecated var only |
| ADR documents | 0 | 4 |
| Test t.Parallel() | disabled | enabled (safe with DI metrics) |
| Audit score | 6/10 | 9/10 |
