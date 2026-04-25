# go-workflow

Standalone DAG workflow engine for Go. 15 step types, MCP server integration, pluggable persistence (file/SQLite/PostgreSQL), distributed execution, templates, approval flows, crash recovery.

**v0.10.0** | 407 tests | Go 1.26 | [MIT License](LICENSE)

## Features

- **DAG execution** — steps run in parallel when dependencies allow
- **15 step types** — adds `vision` for multimodal LLM calls with image inputs (companion to the new `image` step), via optional `VisionCapable` provider interface. Full list: tool, llm, agent, a2a, message, condition, transform, approval, workflow, foreach, branchall, suspend, noop, image, vision
- **MCP integration** — `WithMCPServers()` connects to any MCP server, auto-discovers tools
- **Templates** — parameterized workflow definitions with `{{variable}}` substitution, loaded from JSON files
- **Approval flow** — pause workflow, await human/AI approval, resume or reject
- **Pluggable persistence** — JSON files (default), SQLite, or PostgreSQL
- **Distributed execution** — dispatch steps to remote workers via PostgreSQL SKIP LOCKED queue
- **Production retry** — exponential backoff, per-step timeout, conditional retry/skip, dead letter
- **Crash recovery** — `RecoverAll()` resumes workflows interrupted by process crash
- **Idempotency** — `IdempotencyKey` prevents duplicate workflow runs
- **Security policies** — step budgets, duration limits, tool allow/deny lists, secret masking
- **Watchdog** — auto-detect stalled steps, auto-retry transient failures
- **n8n import** — convert n8n workflow JSON to native templates
- **Scheduler + Cron** — time-based workflow triggers
- **Metrics** — atomic counters for workflows, steps, agents, hooks, triggers
- **Cost tracking** — every LLM and image step contributes to `Workflow.Cost` (tokens, USD, image bytes). Optional `WithBudget(maxUSD)` aborts overrun workflows with `ErrBudgetExceeded`.

## Quick Start

```go
engine := workflow.NewEngine(store,
    workflow.WithMCPServers(map[string]string{
        "go-wp":     "http://127.0.0.1:8894/mcp",
        "go-search": "http://127.0.0.1:8890/mcp",
    }),
    workflow.WithLLMClient(llmClient),
)

engine.RecoverAll(ctx)

wf := workflow.NewWorkflow("wf-1", "Content Pipeline", "ai:claude", []workflow.Step{
    {ID: "research", Kind: workflow.StepTool, Config: map[string]any{
        "tool": "wp_research",
        "args": map[string]any{"topic": "смотровые площадки", "count": 12},
    }},
    {ID: "select", Kind: workflow.StepApproval, Config: map[string]any{
        "message": "Review places and select top 12",
    }, DependsOn: []string{"research"}},
    {ID: "images", Kind: workflow.StepTool, Config: map[string]any{
        "tool": "wp_image",
        "args": map[string]any{"action": "batch"},
    }, DependsOn: []string{"select"}},
})

_ = store.Save(wf)
_ = engine.StartAsync(ctx, "wf-1") // runs in background, pauses at approval
```

## MCP Integration

Connect to any MCP server. Tools are auto-discovered via `ListTools`.

```go
engine := workflow.NewEngine(store,
    workflow.WithMCPServers(map[string]string{
        "wordpress": "http://127.0.0.1:8894/mcp",
        "search":    "http://127.0.0.1:8890/mcp",
        "browser":   "http://127.0.0.1:8901/mcp",
    }),
)

// Steps reference tools by name — routing is automatic:
// "wp_post"       → wordpress server
// "smart_search"  → search server
// "fetch_smart"   → browser server
```

`MCPToolRunner` handles lazy connection, tool discovery, and multi-server routing. Combined with local `ToolRunner` via `MultiToolRunner`.

## Templates

JSON files loaded by `TemplateStore`. Parameters replace `{{key}}` placeholders.

```json
{
  "name": "Create collection: {{topic}}",
  "params": {"topic": "Article topic", "count": "Number of places"},
  "defaults": {"count": 12},
  "steps": [
    {"id": "research", "kind": "tool", "config": {"tool": "wp_research", "args": {"topic": "{{topic}}", "count": "{{count}}"}}},
    {"id": "select",   "kind": "approval", "config": {"message": "Select places"}, "depends_on": ["research"]},
    {"id": "enrich",   "kind": "tool", "config": {"tool": "wp_enrich"}, "depends_on": ["select"]},
    {"id": "compose",  "kind": "approval", "config": {"message": "Write content"}, "depends_on": ["enrich"]},
    {"id": "publish",  "kind": "tool", "config": {"tool": "wp_post"}, "depends_on": ["compose"]}
  ]
}
```

```go
ts := workflow.NewTemplateStore("/path/to/templates")
wf, _ := ts.Instantiate("create-collection", "wf-123", "ai:claude", map[string]any{
    "topic": "смотровые площадки",
    "count": 15,
})
```

## Approval Flow

Steps with `Kind: StepApproval` pause the workflow. Resume via `HandleApproval`:

```go
engine := workflow.NewEngine(store,
    workflow.WithApprovalNotifier(func(wf *workflow.Workflow, step *workflow.Step) {
        // Notify AI/human that approval is needed
        log.Printf("Workflow %s waiting at step %s", wf.ID, step.ID)
    }),
    workflow.WithCompletionNotifier(func(wf *workflow.Workflow) {
        log.Printf("Workflow %s completed", wf.ID)
    }),
)

// Later, when approved:
engine.HandleApproval("wf-123", true)  // or false to reject
engine.ResumeAsync(ctx, "wf-123")
```

## Storage Backends

| Backend | Constructor | Use case |
|---------|-------------|----------|
| JSON files | `store.NewFileStore(dir)` | Development, single-process |
| SQLite | `store.NewSQLiteStore(path)` | Tests, single-binary deployments |
| PostgreSQL | `store.NewPostgresStore(dsn)` | Production, multi-process |

## Step Types

| Kind | Executor | Description |
|------|----------|-------------|
| `tool` | `ToolExecutor` | Call a named tool (local or MCP) |
| `llm` | `LLMExecutor` | Send prompt to LLM, supports tool calling |
| `agent` | `AgentExecutor` | Delegate to a full agent loop |
| `a2a` | `A2AExecutor` | Call remote A2A agent |
| `message` | `MessageExecutor` | Send message to user channel |
| `condition` | `ConditionExecutor` | Branch on expression |
| `transform` | `TransformExecutor` | Transform data between steps |
| `approval` | `ApprovalExecutor` | Pause for human/AI approval |
| `workflow` | `SubWorkflowExecutor` | Run a sub-workflow |
| `foreach` | `ForEachExecutor` | Iterate over collection |
| `branchall` | `BranchAllExecutor` | Run all branches in parallel |
| `suspend` | `SuspendExecutor` | Suspend with TTL |
| `noop` | `NoopExecutor` | Join point, completes immediately |
| `image` | `ImageExecutor` | Render HTML to PNG/JPEG/WebP/SVG via pluggable `ImageRenderer` |
| `vision` | `VisionExecutor` | Multimodal LLM call with image attachments (`VisionCapable` provider) |

## Cost tracking

Every LLM call (`StepLLM`, `StepVision`) and every image render (`StepImage`) updates `Workflow.Cost`:

```go
wf, _ := engine.RunToCompletion(ctx, wf)
fmt.Printf("Total: %d in / %d out tokens, $%.4f, %d images\n",
    wf.Cost.InputTokens, wf.Cost.OutputTokens, wf.Cost.USDEstimate, wf.Cost.ImagesRendered)
```

Optional ceiling: `workflow.WithBudget(0.50)` aborts when the running total exceeds $0.50, surfacing `ErrBudgetExceeded`. Pricing comes from `DefaultCostModel`; override with `WithCostModel(custom)`.

## Retry & Timeout

```go
step := workflow.Step{
    ID:   "flaky-api",
    Kind: workflow.StepTool,
    Config: map[string]any{
        "tool":       "http_request",
        "timeout_ms": 10000,
        "retry": map[string]any{
            "max": 3, "delay_ms": 1000, "backoff_multiplier": 2.0,
            "retry_on": []any{"timeout", "503"},
            "skip_on":  []any{"401", "403"},
        },
    },
}
```

After exhausting retries, steps enter `StepDeadLettered` state — the watchdog will not re-retry them.

## Interfaces

All external dependencies are injected via interfaces:

| Interface | Purpose |
|-----------|---------|
| `StoreBackend` | Persist and load workflows |
| `ToolRunner` | Execute named tools |
| `LLMProvider` | Send prompts to LLM |
| `AgentRunner` | Delegate to agent loop |
| `A2ACaller` | Call remote A2A agents |
| `MessagePublisher` | Deliver messages to users |
| `HookPublisher` | Fire lifecycle events |
| `SkillResolver` | Load skill prompts by name |
| `StepDispatcher` | Route steps to local/remote execution |
| `StepWorkerQueue` | Distributed work queue |
| `StepReaper` | Reclaim steps from dead workers |

## Distributed Execution

Dispatch steps to remote workers via PostgreSQL SKIP LOCKED queue. Features: heartbeat protocol, concurrency limits, LISTEN/NOTIFY, graceful shutdown. Default `LocalDispatcher` preserves in-process execution (zero config change).

## Consumers

- **vaelor** — AI agent orchestrator (Telegram, Discord, A2A)
- **krolik-agent** — lightweight Go agent
- **go-wp** — WordPress content pipeline (planned)
