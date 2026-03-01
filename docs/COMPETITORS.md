# Competitive Landscape

## Positioning

go-workflow occupies a unique niche: **embedded AI-first DAG workflow engine for Go**. No other project combines all of:
- Zero external dependencies (stdlib only, JSON file persistence)
- LLM/Agent/A2A step types as first-class citizens
- n8n workflow import compatibility
- Embeddable as a Go library (no server process)
- Security policy (MaxSteps, MaxDuration, AllowedTools)
- Self-healing watchdog with transient error detection

## Comparison Matrix

| Feature | go-workflow | Temporal | Hatchet | LangGraph | Dify | Windmill | Conductor | Inngest | go-workflows | DBOS Go | Restate |
|---------|------------|----------|---------|-----------|------|----------|-----------|---------|-------------|---------|---------|
| **Language** | Go | Go | Go server | Python | Python | Rust | Java | Go server | Go | Go | Rust |
| **GitHub Stars** | — | 18.5k | 6.7k | 25k | 100k+ | 16k | 31.5k | 5k | 431 | 591 | 3.5k |
| **Embedded** | yes | no | no | yes | no | no | no | no | yes | yes | no |
| **Persistence** | JSON files | Cassandra/PG | Postgres | Pluggable | Postgres | Postgres | Postgres | Postgres | PG/SQLite/Redis | Postgres | WAL journal |
| **DAG model** | DependsOn | code (futures) | Parents/Children | Graph+cycles | visual DAG | visual flow | JSON/YAML | step chains | code (futures) | code (steps) | code (ctx.Run) |
| **Retry** | max + delay_ms | exponential backoff | exponential backoff | manual | workflow-level | conditional | configurable | auto | configurable | per-step | auto |
| **Fan-out** | parallel DependsOn | workflow.Go() | child workflows | Pregel superstep | Iteration node | branchall | fork task | step loops | sub-workflows | manual | virtual objects |
| **Human-in-loop** | StepApproval | signals | — | interrupt() | HumanApproval node | Approval step | Human Task | waitForEvent | — | — | — |
| **LLM steps** | StepLLM | — | — | core | core | — | LLM_TEXT_COMPLETE | — | — | — | — |
| **Agent steps** | StepAgent, StepA2A | — | — | agent nodes | — | — | — | — | — | — | — |
| **Streaming** | — | — | — | token-level | token-level | — | — | — | — | — | — |
| **Cost tracking** | — | — | — | LangSmith | built-in | — | — | — | — | — | — |
| **n8n compat** | aliases+import | — | — | — | — | — | — | — | — | — | — |
| **Watchdog** | auto-recover | — | ticker service | — | — | — | — | — | — | — | — |
| **Security policy** | MaxSteps/Duration/Tools | RBAC/namespaces | — | — | — | workspace | RBAC | — | — | — | — |
| **Ops complexity** | zero | very high | medium | low | medium | medium | high | medium | zero | low | medium |
| **Go native** | library | SDK | SDK | — | — | — | Go SDK | Go SDK | library | library | Go SDK |

## Detailed Analysis

### Tier 1: Direct Competitors (embedded Go workflow)

#### go-workflows (cschleiden/go-workflows) — 431 stars
Temporal-inspired embedded engine. Pluggable backends (memory/SQLite/PG/Redis). Deterministic replay model — all Temporal constraints apply (no native goroutines, no `time.Now()`, no `select`).

**vs go-workflow**: We win on simplicity (no determinism constraints), AI-native steps, n8n compat. They win on durability (event sourcing replay) and multi-backend persistence.

#### DBOS Transact Go — 591 stars
"Postgres IS the workflow engine." Each step = Postgres transaction. Automatic checkpointing. No determinism constraints.

**vs go-workflow**: They win on durability (Postgres transactional checkpointing). We win on zero infrastructure (no Postgres needed), AI steps, security policy.

### Tier 2: Server-based Workflow Engines (Go)

#### Temporal — 18.5k stars
Industry standard for durable execution. Event sourcing replay. Extremely reliable but operationally heavy (3 services + Cassandra/PG + Elasticsearch).

**Key insight**: `ContinueAsNew` for long-running workflows avoids history bloat. 4-level timeout hierarchy (ScheduleToStart/StartToClose/HeartbeatTimeout/ScheduleToClose). Signal/Query API for external interaction.

**vs go-workflow**: Temporal is 100x more durable but 100x more complex. Our niche is embedded single-binary AI agents, not distributed microservices.

#### Hatchet — 6.7k stars
"Temporal for the rest of us." Postgres-only (SKIP LOCKED queue), gRPC streaming, <20ms latency.

**Key insight**: Bidirectional adjacency list (`Parents` + `Children`) in step definitions. `DEAD_LETTERED` status for exhausted retries. `HatchetContext` wraps standard `context.Context`. Concurrency: semaphore + rate limiter per worker.

**vs go-workflow**: Hatchet has better retry policies and Postgres durability. We have AI-native steps and zero infrastructure.

#### Inngest — 5k stars
Event-driven workflow platform. `step.waitForEvent()` for durable HITL. Concurrency per-entity key.

**Key insight**: Flow control syntax — `concurrency: { limit: 1, key: "event.data.userId" }` — is the cleanest rate limiting API. `waitForEvent` with timeout + match expression is powerful for external triggers.

### Tier 3: AI Agent Frameworks

#### LangGraph — 25k stars
Pregel-inspired state graph. Nodes = functions, edges = conditional transitions. Supports cycles (agent loops). Annotated state with merge reducers.

**Key insights worth adopting**:
1. `interrupt_before/after` — declarative HITL without modifying node code
2. Annotated state reducers — explicit merge semantics per field (append vs replace vs sum)
3. `thread_id` isolation — one graph, many parallel execution threads
4. Pluggable checkpointing interface (memory/SQLite/Postgres)

**vs go-workflow**: LangGraph has better state management and checkpointing. We have Go performance (10-50x less orchestration overhead), n8n compat, security policy.

#### Dify — 100k+ stars
Visual workflow builder with LLM nodes, tool integration, knowledge retrieval. Path-based variable selector `['nodeId', 'field']`.

**Key insight**: `HumanApproval` as first-class node type with PENDING/APPROVED/REJECTED states. Iteration node for parallel for-each. `.importlinter` for architectural layer protection.

**vs go-workflow**: Dify has a visual editor and richer AI integration. We have Go embedding, better performance, code-first approach.

#### CrewAI — 45k stars
Role-based multi-agent framework. 5.76x faster than LangGraph. "Flows" layer adds event-driven orchestration with `@listen`/`@start`/`@router` decorators.

**Insight**: Role-based agent design (role + goal + backstory) is effective for high-level task delegation but brittle for precise technical work.

### Tier 4: Traditional Workflow (non-AI reference)

#### Windmill — 16k stars
Rust backend, multi-language scripts in sandboxes. Most ergonomic Approval Step implementation.

**Key insights**: `branchall` (explicit fan-out type), conditional retry (retry only specific errors), `suspend_until` timestamp, `debounce_key` for idempotency, Open Flow specification.

#### Conductor OSS — 31.5k stars
Netflix-origin. Most mature hybrid: JSON workflows + native `LLM_TEXT_COMPLETE`/`LLM_CHAT_COMPLETE` task types + Human Task worker.

**Key insight**: "RL Conductor" — reinforcement-learning trained coordination for multi-LLM workflows. Human Task as dedicated worker type with UI.

#### Restate — 3.5k stars
Durable async/await as sidecar. WASM-based journaling. **No determinism constraints** (unlike Temporal) — the server handles replay transparency.

**Key insight**: Solves Temporal's main pain point. User writes normal Go code with `ctx.Run()` for durable operations. The server journals all I/O.

## Patterns to Adopt

### Priority 1: Production Reliability
| Pattern | Source | Effort | Impact |
|---------|--------|--------|--------|
| Exponential backoff in RetryPolicy | Hatchet, Temporal | S | High |
| Per-step timeout (not just global) | Temporal, Windmill | S | High |
| `StepDeadLettered` status | Hatchet | XS | Medium |
| Conditional retry (retry_on/skip_on error patterns) | Windmill | S | Medium |
| Idempotency key on workflow | Windmill debounce_key | XS | Medium |

### Priority 2: Durability
| Pattern | Source | Effort | Impact |
|---------|--------|--------|--------|
| StoreBackend interface (pluggable persistence) | go-workflows, LangGraph | M | High |
| Postgres backend (SKIP LOCKED queue model) | Hatchet, DBOS | L | High |
| Step checkpointing (resume from last completed) | DBOS, Inngest | M | High |

### Priority 3: AI-Specific
| Pattern | Source | Effort | Impact |
|---------|--------|--------|--------|
| LLM token/cost tracking per step | LangSmith, OTEL GenAI conventions | S | High |
| LLM streaming (token-by-token to caller) | LangGraph, Trigger.dev | M | High |
| Typed variable passing (path-selector) | Dify | M | Medium |
| State reducers (merge semantics per context key) | LangGraph | M | Medium |

### Priority 4: Advanced Execution
| Pattern | Source | Effort | Impact |
|---------|--------|--------|--------|
| ForEach/Map step (parallel iteration) | Dify Iteration, Windmill forloopflow | M | High |
| Event-driven triggers (waitForEvent) | Inngest | M | High |
| Concurrency control per entity key | Inngest | S | Medium |
| Suspend with timeout (suspend_until_ms) | Windmill | S | Medium |
| Graph with cycles (agent loops) | LangGraph | L | Medium |

## Key Architectural Decisions

### Event Sourcing vs Step Checkpointing

Temporal uses event sourcing — replay entire history on failure. Powerful but requires deterministic code. Hatchet/DBOS/Inngest use step checkpointing — store each step result, resume from last completed.

**Decision for go-workflow**: Step checkpointing. Our users write normal Go code calling LLMs and external APIs — determinism constraints would be hostile to the use case.

### Postgres vs JSON Files

Current JSON file persistence is fine for single-agent deployment. Postgres backend enables:
- Concurrent access from multiple processes
- SKIP LOCKED queue for distributed workers
- Transactional step checkpointing
- Better querying (list by state, owner, date range)

**Decision**: Keep JSON as default (zero-ops), add Postgres as optional backend via `StoreBackend` interface.

### Graph vs DAG

LangGraph supports cycles (agent loops). Our current DAG model prevents infinite loops via cycle detection.

**Decision**: Stay DAG-only for v0.x. Cycles add complexity; agent loops in our model are handled by `StepAgent` which delegates to a full agent loop externally.
