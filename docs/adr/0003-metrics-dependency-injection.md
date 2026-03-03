# ADR-0003: Metrics via Dependency Injection

**Status:** Accepted
**Date:** 2026-03-02

## Context

Metrics collection originally used a `GlobalMetrics` singleton. Mutable global state caused race conditions in parallel tests (prevented `t.Parallel()`), made testing harder (shared metrics across test cases), and violates the dependency injection principle adopted elsewhere in the library.

## Decision

Pass `*Metrics` through constructors and functional options (Engine, Scheduler, TriggerService, all step executors). Keep `GlobalMetrics` as a deprecated default fallback for backward compatibility. New code should inject metrics explicitly.

## Consequences

- **Test parallelization:** Tests can safely run with `t.Parallel()` since each test gets its own Metrics instance.
- **Composability:** Components receive metrics as dependencies, making unit tests and mocks simpler.
- **No breaking change:** `GlobalMetrics` remains available; old code continues to work without modification.
- **Clear ownership:** Each component holds a reference to its Metrics; easier to trace instrumentation flow.
- **Path to removal:** `GlobalMetrics` can be deprecated and removed in v1.0 after users migrate.
