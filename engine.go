package workflow

import (
	"log/slog"

	"github.com/anatolykoptev/go-kit/llm"
)

// ApprovalNotifier is called when a workflow needs user approval.
type ApprovalNotifier func(wf *Workflow, step *Step)

// CompletionNotifier is called when a workflow reaches a terminal state.
type CompletionNotifier func(wf *Workflow)

// HookPublisher fires lifecycle events. Satisfied by hooks.Registry.
type HookPublisher interface {
	Fire(event string, data map[string]any) int
}

// Engine orchestrates workflow execution: DAG resolution, step dispatch, persistence.
type Engine struct {
	store              *WorkflowStore
	metrics            *Metrics
	executors          map[StepKind]StepExecutor
	dispatcher         StepDispatcher
	bus                MessagePublisher
	approvalNotifier   ApprovalNotifier
	completionNotifier CompletionNotifier
	hooks              HookPublisher
	listener           *StepListener
	logger             *slog.Logger
	watchdogStop       chan struct{}
	scheduler          *Scheduler
	triggers           *TriggerService
	eventLog           *EventLog
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

func WithMetrics(m *Metrics) EngineOption  { return func(e *Engine) { e.metrics = m } }
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

// NewEngine creates a workflow engine with functional options.
func NewEngine(store *WorkflowStore, opts ...EngineOption) *Engine {
	e := &Engine{
		store:   store,
		metrics: GlobalMetrics,
		logger:  slog.Default(),
		executors: map[StepKind]StepExecutor{
			StepCondition: NewConditionExecutor(),
			StepApproval:  NewApprovalExecutor(),
			StepTransform: NewTransformExecutor(),
		},
	}

	for _, opt := range opts {
		opt(e)
	}

	// Fix metrics for executors created during option application.
	if ex, ok := e.executors[StepLLM].(*LLMExecutor); ok {
		ex.metrics = e.metrics
	}
	if ex, ok := e.executors[StepAgent].(*AgentExecutor); ok {
		ex.metrics = e.metrics
	}
	if ex, ok := e.executors[StepA2A].(*A2AExecutor); ok {
		ex.metrics = e.metrics
	}
	if ex, ok := e.executors[StepImage].(*ImageExecutor); ok {
		ex.metrics = e.metrics
	}

	// Wire ToolRunner into LLMExecutor for tool calling
	if llmEx, ok := e.executors[StepLLM].(*LLMExecutor); ok {
		if toolEx, ok := e.executors[StepTool].(*ToolExecutor); ok {
			llmEx.SetToolRunner(toolEx.runner)
		}
	}

	e.executors[StepWorkflow] = NewSubWorkflowExecutor(e)
	e.executors[StepForEach] = NewForEachExecutor(e)
	e.executors[StepBranchAll] = NewBranchAllExecutor(e)
	e.executors[StepSuspend] = NewSuspendExecutor()
	e.executors[StepNoop] = &NoopExecutor{}

	// Default to in-process dispatch if no dispatcher was provided via options.
	if e.dispatcher == nil {
		e.dispatcher = NewLocalDispatcher(e)
	}

	return e
}

// --- Setters (post-creation wiring) ---

func (e *Engine) SetApprovalNotifier(fn ApprovalNotifier)   { e.approvalNotifier = fn }
func (e *Engine) SetCompletionNotifier(fn CompletionNotifier) { e.completionNotifier = fn }
func (e *Engine) SetHooks(h HookPublisher)                  { e.hooks = h }

func (e *Engine) SetAgentRunner(runner AgentRunner) {
	e.executors[StepAgent] = NewAgentExecutor(runner, e.getMetrics())
}

func (e *Engine) SetSkills(sr SkillResolver) {
	if llm, ok := e.executors[StepLLM].(*LLMExecutor); ok {
		llm.SetSkills(sr)
	}
}

func (e *Engine) SetA2ACaller(caller A2ACaller) {
	e.executors[StepA2A] = NewA2AExecutor(caller, e.getMetrics())
}

// --- Accessors ---

func (e *Engine) getMetrics() *Metrics {
	if e.metrics != nil {
		return e.metrics
	}
	return GlobalMetrics
}

func (e *Engine) log() *slog.Logger {
	if e.logger != nil {
		return e.logger
	}
	return slog.Default()
}

func (e *Engine) Store() *WorkflowStore { return e.store }
