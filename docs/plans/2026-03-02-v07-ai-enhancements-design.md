# v0.7.0 — AI Enhancements Design

## Overview

Five features inspired by LangGraph (state reducers, interrupt), Conductor (LLM task types), and CrewAI (role-based agents). Uses `go-kit/llm` as a direct dependency for streaming and tool calling.

**Dependency**: `github.com/anatolykoptev/go-kit v0.6.0` (llm package).

## Feature 1: State Reducers

### Problem
`wf.Context[step.ID] = result` is always last-write-wins. No append/sum/merge semantics.

### Design
Add `Reducers map[string]ReducerKind` to `Workflow`. Supported kinds:
- `replace` (default) — last write wins
- `append` — appends to `[]any` slice
- `sum` — numeric addition (int64/float64)
- `merge` — shallow merge of `map[string]any`

### Implementation
- New type `ReducerKind string` with constants in `types.go`
- New function `applyReducer(ctx map[string]any, key string, value any, kind ReducerKind)` in new file `reducer.go`
- Modify `engine_step.go` line 157: replace direct assignment with `applyReducer()` call
- `handleSuspend` line 193: same change
- Workflow JSON field: `"reducers": {"messages": "append", "total": "sum"}`

### Files
- `types.go`: add `Reducers` field to `Workflow`, `ReducerKind` type
- `reducer.go`: `applyReducer()` + reducer logic (~60 lines)
- `engine_step.go`: replace context merge loops with reducer calls

## Feature 2: Typed Variable Passing ($ref paths)

### Problem
`resolveRef("$steps.check.result", wf)` only extracts `context["check"]` — no nested field access.

### Design
New path syntax: `"$ref": "step_id.field.subfield"` with support for:
- Dot-path traversal: `fetch.data.name` → `context["fetch"]["data"]["name"]`
- Array indexing: `fetch.items[0]` → first element
- Wildcard projection: `fetch.items[*].name` → extract field from each element

### Implementation
- New function `resolvePath(root any, path string) any` in `resolve.go`
- Parse path into segments (split on `.`, detect `[N]` and `[*]`)
- Walk `map[string]any` / `[]any` tree
- Integrate into `resolveRef()`: after `$steps.` prefix, use full path traversal
- New pattern in step configs: `{"$ref": "step_id.field"}` → executors call `resolveRef` on config values

### Files
- `resolve.go`: add `resolvePath()`, update `resolveRef()` for nested paths
- `resolve_test.go`: path traversal tests

## Feature 3: LLM Streaming

### Problem
`LLMProvider.Chat()` blocks until full response. No token-by-token feedback.

### Design
Use `go-kit/llm.Client` directly. Add streaming support to `LLMExecutor`:
- Step config `{"stream": true}` enables streaming
- Engine option `WithStreamCallback(fn)` receives chunks
- `StreamCallback func(workflowID, stepID string, chunk llm.StreamChunk)`
- Non-streaming path stays unchanged (backward compat)

### Implementation
- Add `github.com/anatolykoptev/go-kit` dependency to go.mod
- New engine option: `WithLLMClient(client *llm.Client)` — stores `*llm.Client` on `LLMExecutor`
- `LLMExecutor` gains `client *llm.Client` and `streamCB StreamCallback` fields
- When `client` is set AND `step.Config["stream"] == true`:
  - Call `client.Stream(ctx, messages)` → iterate `Next()` → call `streamCB` per chunk → collect full content
- When `client` is set without streaming: use `client.Chat(ctx, messages)`
- When only `provider` is set (legacy): use `provider.Chat()` as before
- Token usage from `StreamResponse.Usage()` or `ChatResponse.Usage`

### Interface changes
- `LLMProvider` interface stays (backward compat)
- New `LLMResponse` fields: `ToolCalls []llm.ToolCall`, `FinishReason string`
- `WithLLMProvider()` still works; `WithLLMClient()` is the preferred path

### Files
- `go.mod`: add go-kit dependency
- `engine.go`: add `WithLLMClient()`, `WithStreamCallback()` options
- `executor_llm.go`: add `client` field, streaming branch, go-kit/llm types
- `interfaces.go`: keep `LLMProvider` for compat

## Feature 4: LLM Tool Calling

### Problem
LLM step is single-turn. No ability for LLM to call tools and iterate.

### Design
Multi-turn agentic loop inside `LLMExecutor`:
1. Step config: `{"tools": [...], "max_turns": 5}`
2. Call `client.Chat(messages, WithTools(tools))`
3. If response has `ToolCalls`: execute each via `ToolRunner`, append tool results as messages
4. Loop until content-only response or max_turns exhausted
5. Store final content + all tool call results in context

### Implementation
- `LLMExecutor` gains `toolRunner ToolRunner` field (injected via option)
- Tool definitions in step config map to `llm.Tool` structs
- New method `executeTurn(ctx, messages, tools) (*llm.ChatResponse, error)`
- New method `executeToolCalls(ctx, toolCalls) ([]llm.Message, error)` — calls `ToolRunner.Run()` per tool
- Loop: send → check tool_calls → execute → append → send again
- Context stores: `wf.Context[step.ID]` = final content, `wf.Context[step.ID+"_tool_calls"]` = call log

### Files
- `executor_llm.go`: multi-turn loop, tool execution (~80 lines added)
- `engine.go`: wire `ToolRunner` into `LLMExecutor` when both are configured

## Feature 5: interrupt_before/after

### Problem
No declarative HITL — must add explicit `StepApproval` nodes to pause.

### Design
Workflow-level interrupt lists:
```go
type Workflow struct {
    InterruptBefore []string `json:"interrupt_before,omitempty"`
    InterruptAfter  []string `json:"interrupt_after,omitempty"`
}
```

### Implementation
- In `RunStep()`, after marking step running (line 109), before executor call (line 128):
  - If `stepID ∈ w.InterruptBefore` → set step back to pending, workflow to `StateWaitingApproval`, return
- In `RunStep()`, after success merge (line 162), before logging:
  - If `stepID ∈ w.InterruptAfter` → set workflow to `StateWaitingApproval`, return
- `HandleApproval()` resumes normally — RunToCompletion picks up
- Fire `EventWorkflowApprovalNeeded` hook with `"reason": "interrupt_before"/"interrupt_after"`
- New helper: `slices.Contains(w.InterruptBefore, stepID)`

### Files
- `types.go`: add `InterruptBefore`, `InterruptAfter` fields
- `engine_step.go`: add interrupt checks in `RunStep()`
- `engine_lifecycle.go`: no changes (HandleApproval already works)

## Execution Order

1. **State reducers** + **$ref paths** — foundational, no external deps
2. **LLM streaming** — adds go-kit dep, extends LLMExecutor
3. **LLM tool calling** — builds on streaming, adds multi-turn loop
4. **interrupt_before/after** — independent, can run in parallel with 2-3

## Risk Assessment

- **go-kit dependency**: Same author, stable API. Low risk.
- **LLMProvider backward compat**: Old interface stays. Users upgrade at their pace.
- **State reducers**: Small change in hot path (engine_step.go). Well-tested.
- **Multi-turn tool calling**: Most complex feature. Needs careful timeout/budget handling.
