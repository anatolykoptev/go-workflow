# go-workflow

A standalone DAG workflow engine for Go. Supports 9 step types, pluggable persistence (file/SQLite/PostgreSQL), production retry with exponential backoff, n8n import, template system, security policies, approval flows, watchdog, and metrics.

Extracted from [Vaelor](https://github.com/VaelorAI/Vaelor) for reuse in other bots, MCP servers, and automation tools.

## Features

- **DAG execution** — steps run in parallel when dependencies allow
- **9 step types** — tool, llm, agent, a2a, message, condition, transform, approval, workflow (sub-workflows)
- **Pluggable persistence** — JSON files (default), SQLite, or PostgreSQL via `StoreBackend` interface ([docs](docs/PERSISTENCE.md))
- **Production retry** — exponential backoff, per-step timeout, conditional retry (`retry_on`/`skip_on`), dead letter state
- **Idempotency** — `IdempotencyKey` prevents duplicate workflow runs
- **Crash recovery** — `RecoverAll()` resumes workflows interrupted by process crash
- **n8n compatibility** — import n8n workflow JSON and convert to native templates
- **Template system** — parameterized workflow definitions with `{{variable}}` substitution
- **Security policies** — step budgets, duration limits, tool allow/deny lists, secret masking
- **Approval flow** — pause workflow, await human approval, resume or reject
- **Watchdog** — auto-detect stalled steps, auto-retry transient failures
- **Metrics** — atomic counters for workflows, steps, agents, hooks, triggers

## Quick Start

```go
import workflow "github.com/anatolykoptev/go-workflow"

// Create a store — pick your backend
store, _ := workflow.NewFileStore("/path/to/workflows")       // JSON files (default)
// store, _ := workflow.NewSQLiteStore("/path/to/db.sqlite")  // SQLite
// store, _ := workflow.NewPostgresStore("postgres://...")     // PostgreSQL

// Create engine with functional options
engine := workflow.NewEngine(store,
    workflow.WithToolRunner(myToolRunner),
    workflow.WithLLMProvider(myLLMProvider),
    workflow.WithLogger(slog.Default()),
)

// Recover any workflows interrupted by a previous crash
engine.RecoverAll(context.Background())

// Create and run a workflow
wf := workflow.NewWorkflow("wf-1", "My Workflow", "owner:123", []workflow.Step{
    {ID: "fetch", Kind: workflow.StepTool, Config: map[string]any{
        "tool": "web_fetch",
        "args": map[string]any{"url": "https://example.com"},
    }},
    {ID: "analyze", Kind: workflow.StepLLM, Config: map[string]any{
        "prompt":     "Analyze: {{fetch}}",
        "timeout_ms": 30000,
    }, DependsOn: []string{"fetch"}},
})
wf.IdempotencyKey = "daily-fetch-2024-01-15"

_ = store.Save(wf)
_ = engine.Start(context.Background(), "wf-1")
```

## Storage Backends

Three backends ship out of the box. All implement `StoreBackend` and are interchangeable.

| Backend | Constructor | Use case |
|---------|-------------|----------|
| JSON files | `NewFileStore(dir)` | Development, single-process deployments |
| SQLite | `NewSQLiteStore(path)` | Tests, single-binary deployments |
| PostgreSQL | `NewPostgresStore(dsn)` | Production, multi-process access |

See [docs/PERSISTENCE.md](docs/PERSISTENCE.md) for schema, configuration, and custom backend guide.

## Retry & Timeout

Configure per step via `Config["retry"]` and `Config["timeout_ms"]`:

```go
step := workflow.Step{
    ID:   "flaky-api",
    Kind: workflow.StepTool,
    Config: map[string]any{
        "tool": "http_request",
        "timeout_ms": 10000,  // 10s per-step timeout
        "retry": map[string]any{
            "max":                3,
            "delay_ms":          1000,
            "backoff_multiplier": 2.0,   // 1s → 2s → 4s
            "max_delay_ms":      10000,  // cap at 10s
            "retry_on":  []any{"timeout", "503"},  // only retry these
            "skip_on":   []any{"401", "403"},       // never retry these
        },
    },
}
```

After exhausting retries, steps enter `StepDeadLettered` state (distinct from `StepFailed`) — the watchdog will not re-retry them.

## Interfaces

All external dependencies are injected via interfaces:

- `StoreBackend` — persist and load workflows
- `ToolRunner` — execute named tools
- `LLMProvider` — send prompts to an LLM
- `AgentRunner` — delegate tasks to a full agent loop
- `A2ACaller` — call remote A2A agents
- `MessagePublisher` — deliver messages to users
- `HookPublisher` — fire lifecycle events
- `SkillResolver` — load skill prompts by name

## License

MIT
