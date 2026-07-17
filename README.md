# go-workflow

Standalone DAG workflow engine for Go. 15 step types, MCP server integration, pluggable persistence (file/SQLite/PostgreSQL), distributed execution, templates, approval flows, crash recovery.

Go 1.26 · [Apache-2.0](LICENSE) · [Releases](https://github.com/anatolykoptev/go-workflow/releases)

## Installation

```
go get github.com/anatolykoptev/go-workflow
```

## Features

- **DAG execution** — steps run in parallel when dependencies allow
- **15 step types** — tool, llm, agent, a2a, message, condition, transform, approval, workflow, foreach, branchall, suspend, noop, image, and vision (multimodal LLM with image inputs via the optional `VisionCapable` interface)
- **MCP integration** — `WithMCPServers()` connects to any MCP server, auto-discovers tools
- **Templates** — parameterized workflow definitions with `{{variable}}` (string) and `"@@int:NAME"` / `"@@bool:NAME"` / `"@@float:NAME"` (typed) substitution, loaded from JSON files
- **Approval flow** — pause workflow, await human/AI approval, resume or reject
- **Pluggable persistence** — JSON files (default), SQLite, or PostgreSQL
- **Distributed execution** — dispatch steps to remote workers via PostgreSQL SKIP LOCKED queue
- **Production retry** — exponential backoff with ±25% jitter, `Retry-After` honoring, per-step timeout, conditional retry/skip, dead letter
- **Circuit breaking** — per-endpoint circuit breaker on outbound tool / agent / MCP / A2A / vision calls; fails fast when an endpoint is down, recovers on a half-open probe
- **Rate limiting** — opt-in per-provider QPS token-bucket on outbound calls via `WithRateLimit(provider, rate, burst)`
- **Crash recovery** — `RecoverAll()` resumes workflows interrupted by process crash
- **Idempotency** — `IdempotencyKey` prevents duplicate workflow runs
- **Security policies** — step budgets, duration limits, tool allow/deny lists, secret masking
- **Watchdog** — auto-detect stalled steps, auto-retry transient failures
- **n8n import** — convert n8n workflow JSON to native templates
- **Scheduler + Cron** — time-based workflow triggers
- **Metrics** — atomic counters for workflows, steps, agents, hooks, triggers
- **Cost tracking** — every LLM and image step contributes to `Workflow.Cost` (tokens, USD, image bytes). Optional `WithBudget(maxUSD)` aborts overrun workflows with `ErrBudgetExceeded`.
- **OpenTelemetry tracing** — `WithTracerProvider(tp)` emits a `workflow.run` span per workflow and a `step.<kind>` span per step, with `step.duration_ms`, `step.cache_hit`, cost, and classified error attributes. Wires any OTel-compatible backend (Jaeger, Tempo, Honeycomb, Datadog, OTLP).
- **Webhook triggers** — `Engine.RegisterWebhooks(mux, runtime, []WebhookTrigger{...})` instantiates a template from an HTTP POST. Supports bearer-token and HMAC-SHA256 (GitHub-style) auth with constant-time comparison; body cap 10 MiB; default JSON `VarMapper` overridable per trigger.
- **Step caching** — `WithStepCache(cache) / WithStepCacheKinds(...)` skips deterministic steps when the same (kind, config, depends_on) hash has been seen before. `InMemoryCache` and `FileCache` ship in-package; cache hits replay both Output and Cost so accounting stays accurate.

## Quick Start

```go
engine := workflow.NewEngine(store,
    workflow.WithMCPServers(map[string]string{
        "content-server": "http://127.0.0.1:8080/mcp",
        "search-service": "http://127.0.0.1:8081/mcp",
    }),
    workflow.WithLLMClient(llmClient),
)

engine.RecoverAll(ctx)

wf := workflow.NewWorkflow("wf-1", "Content Pipeline", "ai:claude", []workflow.Step{
    {ID: "research", Kind: workflow.StepTool, Config: map[string]any{
        "tool": "search_web",
        "args": map[string]any{"topic": "best viewpoints in the city", "count": 12},
    }},
    {ID: "select", Kind: workflow.StepApproval, Config: map[string]any{
        "message": "Review results and select top 12",
    }, DependsOn: []string{"research"}},
    {ID: "images", Kind: workflow.StepTool, Config: map[string]any{
        "tool": "get_images",
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
        "my-tools":       "http://127.0.0.1:8080/mcp",
        "search-service": "http://127.0.0.1:8081/mcp",
        "browser":        "http://127.0.0.1:8082/mcp",
    }),
)

// Steps reference tools by name — routing is automatic:
// "create_item"   → my-tools server
// "smart_search"  → search-service server
// "fetch_page"    → browser server
```

`MCPToolRunner` handles lazy connection, tool discovery, and multi-server routing. Combined with local `ToolRunner` via `MultiToolRunner`.

## Templates

JSON files loaded by `TemplateStore`. IDE / Claude validation via JSON Schema:

```json
{ "$schema": "https://raw.githubusercontent.com/anatolykoptev/go-workflow/main/docs/template.schema.json" }
```

See [`docs/template.schema.json`](docs/template.schema.json) for the full schema reference.

### Substitution

- **`{{key}}`** — string substitution. The surrounding JSON quotes are preserved; result is always a JSON string.
- **Typed ParamSpec** *(preferred)* — declare `"type": "int"` (or `bool`, `float`) on the param; engine coerces the value before substitution and strips surrounding quotes automatically:

  ```json
  "params": {
    "count":   {"type": "int",   "description": "Number of places", "default": 12},
    "verbose": {"type": "bool",  "description": "Verbose log flag",  "default": false}
  }
  ```

  Then `"count": "{{count}}"` in a step config becomes `"count": 12` after substitution (bare integer).

- **`"@@int:KEY"` / `"@@bool:KEY"` / `"@@float:KEY"`** *(deprecated since v0.13)* — typed substitution via magic markers. Quotes are stripped after substitution. Emits a deprecation log per match pointing to the typed ParamSpec form above.

```json
{
  "name": "Content pipeline: {{topic}}",
  "params": {"topic": "Article topic", "count": "Number of places", "verbose": "Verbose log flag"},
  "defaults": {"count": "12", "verbose": "false"},
  "steps": [
    {"id": "research", "kind": "tool", "config": {"tool": "search_web", "args": {"topic": "{{topic}}", "count": "@@int:count", "verbose": "@@bool:verbose"}}},
    {"id": "select",   "kind": "approval", "config": {"message": "Select items"}, "depends_on": ["research"]},
    {"id": "enrich",   "kind": "tool", "config": {"tool": "update_metadata"}, "depends_on": ["select"]},
    {"id": "compose",  "kind": "approval", "config": {"message": "Write content"}, "depends_on": ["enrich"]},
    {"id": "publish",  "kind": "tool", "config": {"tool": "create_post"}, "depends_on": ["compose"]}
  ]
}
```

```go
ts := workflow.NewTemplateStore("/path/to/templates")
wf, _ := ts.Instantiate("create-collection", "wf-123", "ai:claude", map[string]any{
    "topic":   "city viewpoints",
    "count":   "15",     // typed @@int:count emits bare 15 in JSON
    "verbose": "true",   // typed @@bool:verbose emits bare true
})
```

After substitution `args.count` is the JSON integer `15` (not the string `"15"`); `args.verbose` is the JSON boolean `true`. Coercion errors (e.g. `@@int:` against a non-numeric value) surface from `ResolveRefsErr`; the legacy `ResolveRefs` logs and continues for backward compat.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `WORKFLOW_TOOL_API_URL` | Base URL of the tool API server. HTTP request nodes that call `/tools/execute` or `/agent/run` on this URL are detected and converted to native tool/agent steps by the n8n importer. Example: `http://127.0.0.1:8080` |


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
engine.HandleApproval("wf-123", true, "")  // step_id "" = auto-resolve the blocking gate; or pass a specific step id
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
| `StepExecutor` | Execute a single workflow step |
| `StepCache` | Cache deterministic step results |
| `StepEnqueuer` | Enqueue steps for distributed execution |
| `ImageRenderer` | Render HTML to image formats |
| `SubWorkflowRunner` | Run nested sub-workflows |
| `VisionCapable` | Multimodal LLM capability probe |

## Distributed Execution

Dispatch steps to remote workers via PostgreSQL SKIP LOCKED queue. Features: heartbeat protocol, concurrency limits, LISTEN/NOTIFY, graceful shutdown. Default `LocalDispatcher` preserves in-process execution (zero config change).
