# ADR-0001: Flat Package with Subpackages

**Status:** Accepted
**Date:** 2026-03-02

## Context

The workflow library originally had all code in a single flat `package workflow` directory. This simplified the API surface but created transitive dependency pollution—consumers importing the core `workflow` package were pulled in with pgx, sqlite3, and other backend dependencies even if they only needed domain types and the engine.

## Decision

Keep domain types (Step, Workflow, Execution, etc.) and the core Engine in the root `workflow` package. Extract storage backend implementations (FileStore, SQLiteStore, PostgresStore) into a dedicated `store/` subpackage. Defer further extraction (e.g., `engine/`, `trigger/`) to v1.0 after the API stabilizes.

## Consequences

- **Minimal transitive dependencies:** Consumers can import only `github.com/anatolykoptev/go-workflow` without pulling in backend drivers.
- **Backend isolation:** Storage backends are cleanly decoupled and can be imported only when needed.
- **API break:** Constructors like `NewFileStore`, `NewSQLiteStore`, and `NewPostgresStore` moved to the `store/` subpackage; users must update imports.
- **Clear separation:** Root package = core domain + contracts; `store/` = concrete implementations.
- **Future extensibility:** New backends can be added to `store/` without polluting the root package.
