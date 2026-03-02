# ADR-0004: Executor Per-File Organization

**Status:** Accepted
**Date:** 2026-03-02

## Context

The original `executors.go` file contained 625 lines spanning 9 different executor types (Function, Foreach, Conditional, etc.). This violated the Single Responsibility Principle and made navigation, testing, and maintenance difficult. The file was a god file that mixed distinct concerns.

## Decision

Organize executors following a `executor_<kind>.go` naming convention (e.g., `executor_function.go`, `executor_foreach.go`). Extract common utilities into `resolve.go` (dependency resolution, binding). Define the `StepExecutor` interface once in `interfaces.go`. Each executor file contains 30–140 lines focused on that executor's logic.

## Consequences

- **Single Responsibility:** Each file has one purpose—clear ownership per executor type.
- **Maintainability:** Changes to one executor type are isolated; easier to review and test.
- **Discoverability:** Users and contributors quickly locate executor logic by file name.
- **Reduced cognitive load:** Each file is small enough to fit in working memory.
- **Pattern consistency:** Follows the same organizational pattern already used for triggers, tasks, and other domain entities.
