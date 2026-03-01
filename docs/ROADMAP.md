# go-workflow Roadmap

## v0.1.0 — Extraction (done)
- [x] Extract from Vaelor pkg/workflow
- [x] Replace Vaelor deps with local interfaces (MessagePublisher, LLMProvider)
- [x] Functional options for Engine construction
- [x] slog for logging (injected)
- [x] All 77 tests passing, 0 lint issues
- [x] Vaelor migrated to go-workflow

## v0.2.0 — Production Retry & Timeout

Inspired by: Temporal (timeout hierarchy), Hatchet (exponential backoff, DEAD_LETTERED), Windmill (conditional retry).

- [x] Exponential backoff: `BackoffMultiplier` field in retry config, `min * mult^attempt` capped at `MaxDelay`
- [x] Per-step timeout: `timeout_ms` in step config, context deadline per RunStep
- [x] `StepDeadLettered` state: after exhausted retries, distinct from `StepFailed` — watchdog won't re-retry
- [x] Conditional retry: `retry_on` / `skip_on` string patterns in step config (extends existing `IsTransientError`)
- [x] `IdempotencyKey` field on Workflow: prevent duplicate runs of same template+params

## v0.3.0 — Pluggable Persistence

Inspired by: go-workflows (multi-backend), DBOS (Postgres transactional checkpointing), Hatchet (SKIP LOCKED queue).

- [x] `StoreBackend` interface: `Save`, `Load`, `Delete`, `List`, `Modify` — extracted from current WorkflowStore
- [x] JSON file backend (current behavior, default)
- [x] PostgreSQL backend: JSONB snapshots, SELECT FOR UPDATE, partial unique index for idempotency
- [x] Step checkpointing: on crash, resume from last completed step (RecoverAll)
- [x] SQLite backend (for tests and single-binary deployments)

## v0.4.0 — Cron & Event Triggers

Inspired by: Hatchet (TriggeredByCron/ByEvent), Inngest (waitForEvent with match expression).

- [ ] Extract cron/scheduler from Vaelor pkg/cron
- [ ] Time-based triggers: `at` (one-shot), `every` (interval), `cron` (expression)
- [ ] Event-based triggers: hook event → auto-start workflow
- [ ] `WaitForEvent` step type: suspend until matching event arrives (with timeout)
- [ ] Trigger→workflow auto-start with parameter injection

## v0.5.0 — Observability

Inspired by: Temporal (visibility), Rivet (trace viewer), OTEL GenAI conventions.

- [ ] OpenTelemetry tracing: span per step with `gen_ai.*` attributes for LLM steps
- [ ] Token/cost tracking: input_tokens, output_tokens, model per LLM step execution
- [ ] Prometheus metrics exporter (WorkflowsCreated, StepsExecuted, LLMTokensUsed, etc.)
- [ ] Structured event log: append-only JSONL per workflow (inputs, outputs, timing, errors)
- [ ] Execution replay: load event log → reconstruct full execution trace

## v0.6.0 — Advanced DAG

Inspired by: Windmill (branchall, forloopflow), Dify (Iteration node), Argo (withParam fan-out).

- [ ] `StepForEach`: iterate over list, execute sub-steps per item (parallel or sequential)
- [ ] `StepBranchAll`: explicit fan-out — run N branches in parallel, collect all results
- [ ] Dynamic step generation: step output → new steps added at runtime
- [ ] Per-step concurrency limit: max N parallel instances of a ForEach step
- [ ] `suspend_until_ms`: timed pause — watchdog auto-resumes after deadline

## v0.7.0 — AI Enhancements

Inspired by: LangGraph (state reducers, interrupt), Conductor (LLM task types), CrewAI (role-based agents).

- [ ] State reducers: per-context-key merge semantics (append, replace, sum) instead of last-write-wins
- [ ] Typed variable passing: `{"$ref": "step_id.field.subfield"}` path-selector for step inputs
- [ ] LLM streaming: token-by-token callback from LLMProvider to caller
- [ ] LLM tool calling in workflow: LLM step can invoke tools, multi-turn within single step
- [ ] `interrupt_before/after`: declarative HITL — pause before/after named steps without code change

## v0.8.0 — Distributed Execution

Inspired by: Hatchet (Postgres queue + gRPC workers), Temporal (task queues), Restate (virtual objects).

- [ ] Worker interface: steps dispatched to remote workers via queue
- [ ] Postgres SKIP LOCKED as work queue (no Redis/Kafka dependency)
- [ ] Heartbeat protocol: workers report progress, engine detects dead workers
- [ ] Concurrency control per entity key (Inngest pattern): `limit: N, key: "owner"`
- [ ] Graceful shutdown: PauseAll → drain workers → StopWatchdog

## Future
- [ ] Visual DAG editor (web UI) — React + WebSocket for live execution view
- [ ] Workflow versioning & migration (Temporal getVersion pattern)
- [ ] Graph with cycles for agent loops (LangGraph Pregel model)
- [ ] Open Flow specification compatibility (Windmill standard)
- [ ] RL-trained step routing (Conductor RL pattern)
