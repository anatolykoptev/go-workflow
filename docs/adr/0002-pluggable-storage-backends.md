# ADR-0002: Pluggable Storage Backends via Interface

**Status:** Accepted
**Date:** 2026-03-02

## Context

The engine originally supported only in-memory and file-based storage. Production deployments require persistence options like SQLite (for single-server apps) and PostgreSQL (for distributed systems). Adding each new storage type directly to the core required tight coupling.

## Decision

Define a `StoreBackend` interface in the root `workflow` package with methods: `Save`, `Load`, `Delete`, `List`, `ListByOwner`, `FindByIdempotencyKey`, `Modify`, `Close`. Provide three concrete implementations in the `store/` subpackage: FileBackend, SQLiteBackend, and PostgresBackend. Wrap all backends with a `WorkflowStore` that handles clone-on-entry/exit semantics and ensures isolation between workflow instances.

## Consequences

- **Extensibility:** New backends can be added without modifying the Engine or core interfaces.
- **Decoupling:** Storage logic is completely isolated from business logic.
- **Conformance:** A shared test suite validates all backends against the same contract.
- **Embedded migrations:** Each backend includes SQL migrations (for SQLite/Postgres) bundled in the binary.
- **Operational simplicity:** Users choose a backend by instantiating the right constructor (e.g., `store.NewPostgresStore()`) and passing it to the Engine.
