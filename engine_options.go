package workflow

import (
	"log/slog"

	"github.com/anatolykoptev/go-kit/llm"
)

// EngineOption configures an Engine. Options apply during NewEngine; some
// options also have post-creation Set* twins on Engine for late binding (see
// engine_setters.go).
type EngineOption func(*Engine)

func WithMetrics(m *Metrics) EngineOption    { return func(e *Engine) { e.metrics = m } }
func WithLogger(l *slog.Logger) EngineOption { return func(e *Engine) { e.logger = l } }

func WithHookPublisher(h HookPublisher) EngineOption {
	return func(e *Engine) { e.hooks = h }
}

func WithMessagePublisher(m MessagePublisher) EngineOption {
	return func(e *Engine) {
		e.bus = m
		e.executors[StepMessage] = NewMessageExecutor(m)
	}
}

func WithLLMProvider(p LLMProvider) EngineOption {
	return func(e *Engine) {
		e.executors[StepLLM] = NewLLMExecutor(p, e.metrics)
		// Auto-wire vision executor when the same provider declares multimodal
		// support. A separate vision-only provider can still be registered via
		// WithVisionProvider, which overrides this default.
		if vc, ok := p.(VisionCapable); ok && vc.SupportsVision() {
			if _, exists := e.executors[StepVision]; !exists {
				e.executors[StepVision] = NewVisionExecutor(p, e.metrics)
			}
		}
	}
}

// WithVisionProvider registers a multimodal LLM provider for the StepVision
// primitive. Use this when a separate vision-capable provider is desired
// (e.g. Claude Opus for vision, Sonnet for general LLM steps), or when the
// same provider should serve both LLM and vision steps. When the same
// provider is passed to both WithLLMProvider and WithVisionProvider, the
// later option wins for the vision executor.
func WithVisionProvider(p LLMProvider) EngineOption {
	return func(e *Engine) {
		e.executors[StepVision] = NewVisionExecutor(p, e.metrics)
	}
}

func WithLLMClient(c *llm.Client) EngineOption {
	return func(e *Engine) {
		e.executors[StepLLM] = NewLLMExecutorWithClient(c, e.metrics)
	}
}

func WithStreamCallback(cb StreamCallback) EngineOption {
	return func(e *Engine) {
		if ex, ok := e.executors[StepLLM].(*LLMExecutor); ok {
			ex.SetStreamCallback(cb)
		}
	}
}

func WithToolRunner(t ToolRunner) EngineOption {
	return func(e *Engine) {
		e.executors[StepTool] = NewToolExecutor(t)
	}
}

func WithMCPServers(servers map[string]string) EngineOption {
	return func(e *Engine) {
		mcpRunner := NewMCPToolRunner(servers)
		if existing, ok := e.executors[StepTool].(*ToolExecutor); ok {
			multi := NewMultiToolRunner(existing.runner, mcpRunner)
			e.executors[StepTool] = NewToolExecutor(multi)
		} else {
			e.executors[StepTool] = NewToolExecutor(mcpRunner)
		}
	}
}

// WithMCPServerHeaders sets HTTP headers (e.g. Authorization) for a specific MCP server.
// Must be called after WithMCPServers.
func WithMCPServerHeaders(serverID string, headers map[string]string) EngineOption {
	return func(e *Engine) {
		if te, ok := e.executors[StepTool].(*ToolExecutor); ok {
			setMCPHeaders(te.runner, serverID, headers)
		}
	}
}

func setMCPHeaders(runner ToolRunner, serverID string, headers map[string]string) {
	switch r := runner.(type) {
	case *MCPToolRunner:
		r.SetHeaders(serverID, headers)
	case *MultiToolRunner:
		for _, nr := range r.runners {
			setMCPHeaders(nr.runner, serverID, headers)
		}
	}
}

// setMCPBreakers wires a breakerRegistry into all MCPToolRunner instances
// reachable through the given ToolRunner (handles multi-runner wrapping).
func setMCPBreakers(runner ToolRunner, reg *breakerRegistry) {
	switch r := runner.(type) {
	case *MCPToolRunner:
		r.breakers = reg
	case *MultiToolRunner:
		for _, nr := range r.runners {
			setMCPBreakers(nr.runner, reg)
		}
	}
}

func WithAgentRunner(a AgentRunner) EngineOption {
	return func(e *Engine) {
		e.executors[StepAgent] = NewAgentExecutor(a, e.metrics)
	}
}

func WithA2ACaller(c A2ACaller) EngineOption {
	return func(e *Engine) {
		e.executors[StepA2A] = NewA2AExecutor(c, e.metrics)
	}
}

func WithSkillResolver(s SkillResolver) EngineOption {
	return func(e *Engine) {
		if llm, ok := e.executors[StepLLM].(*LLMExecutor); ok {
			llm.SetSkills(s)
		}
	}
}

func WithApprovalNotifier(fn ApprovalNotifier) EngineOption {
	return func(e *Engine) { e.approvalNotifier = fn }
}

func WithCompletionNotifier(fn CompletionNotifier) EngineOption {
	return func(e *Engine) { e.completionNotifier = fn }
}

func WithScheduler(s *Scheduler) EngineOption {
	return func(e *Engine) { e.scheduler = s }
}

func WithTriggers(ts *TriggerService) EngineOption {
	return func(e *Engine) { e.triggers = ts }
}

func WithEventLog(el *EventLog) EngineOption {
	return func(e *Engine) { e.eventLog = el }
}

// WithCostModel overrides the default per-model USD pricing table.
// Useful to add new models or apply discounted contract pricing.
// Map is shallow-copied — caller can mutate after.
func WithCostModel(model map[string]ModelPrice) EngineOption {
	return func(e *Engine) {
		if model == nil {
			return // no-op: keep existing cost model (e.g. DefaultCostModel)
		}
		e.costModel = make(map[string]ModelPrice, len(model))
		for k, v := range model {
			e.costModel[k] = v
		}
	}
}

// WithBudget sets a maximum USD ceiling per workflow. When exceeded, the
// next cost-bearing step returns ErrBudgetExceeded and the workflow fails.
// 0 (default) means unlimited.
func WithBudget(maxUSD float64) EngineOption {
	return func(e *Engine) {
		e.budgetUSD = maxUSD
	}
}

func WithStepListener(l *StepListener) EngineOption {
	return func(e *Engine) { e.listener = l }
}

// WithImageRenderer registers a backend renderer for the StepImage primitive.
// When set, the engine accepts steps of kind "image". Without it, image steps
// are rejected at validation time with an "unknown step kind" error.
//
// Apply this option BEFORE WithImageWorkspace — the workspace option mutates
// the executor created here, so it must already exist.
func WithImageRenderer(r ImageRenderer) EngineOption {
	return func(e *Engine) {
		e.executors[StepImage] = NewImageExecutor(r, e.metrics)
	}
}

// WithImageWorkspace makes the image executor persist rendered bytes to disk
// under the given directory. Each rendered step writes
// <workspaceDir>/<workflow_id>/<step_id>.<ext> and the path appears in the
// step's result map for downstream reference.
//
// Must be applied AFTER WithImageRenderer; it is a no-op when the image
// executor has not been registered.
func WithImageWorkspace(dir string) EngineOption {
	return func(e *Engine) {
		if ex, ok := e.executors[StepImage].(*ImageExecutor); ok {
			ex.workspaceDir = dir
		}
	}
}
