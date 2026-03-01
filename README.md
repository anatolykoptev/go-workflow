# go-workflow

A standalone DAG workflow engine for Go. Supports 9 step types, n8n import, template system, security policies, approval flows, watchdog, and metrics.

Extracted from [Vaelor](https://github.com/VaelorAI/Vaelor) for reuse in other bots, MCP servers, and automation tools.

## Features

- **DAG execution** — steps run in parallel when dependencies allow
- **9 step types** — tool, llm, agent, a2a, message, condition, transform, approval, workflow (sub-workflows)
- **n8n compatibility** — import n8n workflow JSON and convert to native templates
- **Template system** — parameterized workflow definitions with `{{variable}}` substitution
- **Security policies** — step budgets, duration limits, tool allow/deny lists, secret masking
- **Approval flow** — pause workflow, await human approval, resume or reject
- **Watchdog** — auto-detect stalled steps, auto-retry transient failures
- **Metrics** — atomic counters for workflows, steps, agents, hooks, triggers
- **Zero external deps** — stdlib only

## Usage

```go
import "github.com/anatolykoptev/go-workflow"

// Create a store (JSON file persistence)
store, _ := workflow.NewWorkflowStore("/path/to/workflows")

// Create engine with functional options
engine := workflow.NewEngine(store,
    workflow.WithToolRunner(myToolRunner),
    workflow.WithLLMProvider(myLLMProvider),
    workflow.WithLogger(slog.Default()),
)

// Create and run a workflow
wf := workflow.NewWorkflow("wf-1", "My Workflow", "owner:123", []workflow.Step{
    {ID: "fetch", Kind: workflow.StepTool, Config: map[string]any{"tool": "web_fetch", "args": map[string]any{"url": "https://example.com"}}},
    {ID: "analyze", Kind: workflow.StepLLM, Config: map[string]any{"prompt": "Analyze: {{fetch}}"}, DependsOn: []string{"fetch"}},
})
_ = store.Save(wf)
_ = engine.Start(context.Background(), "wf-1")
```

## Interfaces

All external dependencies are injected via interfaces:

- `ToolRunner` — execute named tools
- `LLMProvider` — send prompts to an LLM
- `AgentRunner` — delegate tasks to a full agent loop
- `A2ACaller` — call remote A2A agents
- `MessagePublisher` — deliver messages to users
- `HookPublisher` — fire lifecycle events
- `SkillResolver` — load skill prompts by name

## License

MIT
