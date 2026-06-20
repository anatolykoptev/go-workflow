# ADR-0004: Executor Per-File Organization

**Status:** Accepted
**Date:** 2026-03-02

## Context

The original executor logic was consolidated in a single file, violating the Single Responsibility Principle. The codebase spans multiple executor types: agent, a2a, approval, branchall, condition, foreach, image, llm, message, mcp, subworkflow, suspend, tool, transform, and vision. This made navigation, testing, and maintenance difficult.

## Decision

Organize executors following a `executor_<kind>.go` naming convention (e.g., `executor_agent.go`, `executor_foreach.go`, `executor_llm.go`). Extract common utilities into `resolve.go` (dependency resolution, binding). Define the `StepExecutor` interface once in `interfaces.go`. Each executor file (e.g., `executor_tool.go`, `executor_agent.go`, `executor_vision.go`) contains 30–140 lines focused on that executor's logic. Step kinds supported: `StepAgent`, `StepTool`, `StepLLM`, `StepA2A`, `StepApproval`, `StepCondition`, `StepTransform`, `StepMessage`, `StepWorkflow`, `StepForEach`, `StepBranchAll`, `StepSuspend`, `StepNoop`, `StepImage`, `StepVision`.

## Consequences

- **Single Responsibility:** Each file has one purpose—clear ownership per executor type.
- **Maintainability:** Changes to one executor type are isolated; easier to review and test.
- **Discoverability:** Users and contributors quickly locate executor logic by file name.
- **Reduced cognitive load:** Each file is small enough to fit in working memory.
- **Pattern consistency:** Follows the same organizational pattern already used for triggers, tasks, and other domain entities.
