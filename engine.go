// Package workflow — Engine ties everything together: it owns the executor
// map, the persistence store, lifecycle notifiers, cost accounting, and the
// dispatcher. The Engine type itself lives here, alongside the small set of
// types it depends on. See companion files for the rest of the engine's
// surface area:
//
//   - engine_options.go  — WithX functional options (configuration DSL)
//   - engine_setters.go  — Set* methods for post-NewEngine wiring
//   - engine_validate.go — ValidateWorkflow + ValidateTemplate pre-flight checks
//   - step_cache.go      — WithStepCache / WithStepCacheKinds
//   - tracing.go         — WithTracerProvider
//   - dispatcher.go      — WithDispatcher
package workflow

import (
	"errors"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// ErrBudgetExceeded is returned by cost-bearing executors when a workflow's
// running USD total exceeds the engine's configured budget. Workflows that
// trigger this error fail; partial cost is preserved on Workflow.Cost.
var ErrBudgetExceeded = errors.New("workflow budget exceeded")

// usdToMicrocents scales a dollars float to integer micro-cents (USD * 1e6)
// with rounding. Microcents preserve sub-cent precision so cheap models
// (Haiku/Flash, where a single call may cost <$0.01) still move the metric.
const (
	usdToMicrocents = 1_000_000.0
	usdRoundHalf    = 0.5
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
	costModel          map[string]ModelPrice
	budgetUSD          float64 // 0 = no budget
	rateLimits         *rateLimitRegistry
	breakers           *breakerRegistry
	tracerProvider     trace.TracerProvider
	tracer             trace.Tracer
	stepCache          StepCache
	stepCacheKinds     map[StepKind]bool
}

// NewEngine creates a workflow engine with functional options. The default
// executor set covers the dependency-free step kinds (condition, approval,
// transform). Other kinds are registered by the corresponding WithX option:
// WithLLMProvider for LLM/vision, WithToolRunner / WithMCPServers for tool,
// WithImageRenderer for image, etc. Sub-workflow / foreach / branchall /
// suspend / noop executors are wired automatically here since they need a
// pointer back to the engine.
func NewEngine(store *WorkflowStore, opts ...EngineOption) *Engine {
	e := &Engine{
		store:     store,
		metrics:   GlobalMetrics,
		logger:    slog.Default(),
		costModel: DefaultCostModel,
		breakers:  &breakerRegistry{},
		executors: map[StepKind]StepExecutor{
			StepCondition: NewConditionExecutor(),
			StepApproval:  NewApprovalExecutor(),
			StepTransform: NewTransformExecutor(),
		},
	}

	for _, opt := range opts {
		opt(e)
	}

	// Fix metrics for executors created during option application — options
	// run before e.metrics may have been overridden by WithMetrics.
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
		ex.engine = e
	}
	if ex, ok := e.executors[StepVision].(*VisionExecutor); ok {
		ex.metrics = e.metrics
		ex.engine = e
	}
	if ex, ok := e.executors[StepLLM].(*LLMExecutor); ok {
		ex.engine = e
	}

	// Wire engine-scoped breaker registry into all outbound executors.
	// This keeps breaker state per-Engine (not global), preventing test bleed.
	if ex, ok := e.executors[StepTool].(*ToolExecutor); ok {
		ex.breakers = e.breakers
		setMCPBreakers(ex.runner, e.breakers)
	}
	if ex, ok := e.executors[StepAgent].(*AgentExecutor); ok {
		ex.breakers = e.breakers
	}
	if ex, ok := e.executors[StepA2A].(*A2AExecutor); ok {
		ex.breakers = e.breakers
	}
	if ex, ok := e.executors[StepVision].(*VisionExecutor); ok {
		ex.breakers = e.breakers
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

// recordStepCost is called by executors that bear cost. It computes USD,
// merges into the workflow's WorkflowCost aggregate, increments global
// metrics, and returns ErrBudgetExceeded if the new total exceeds the
// engine's budget. Safe to call when wf is nil (no-op).
func (e *Engine) recordStepCost(wf *Workflow, c StepCost) error {
	if wf == nil {
		return nil
	}
	c.USDEstimate = EstimateUSD(c.Model, c.InputTokens, c.OutputTokens, e.costModel)
	wf.AddCost(c)

	if m := e.getMetrics(); m != nil {
		m.WorkflowTokensInputTotal.Add(c.InputTokens)
		m.WorkflowTokensOutputTotal.Add(c.OutputTokens)
		// Store dollars as micro-cents (USD * 1e6, rounded) since
		// atomic.Float64 doesn't exist. Microcents preserve sub-cent
		// precision so cheap-model calls still move the metric.
		if c.USDEstimate > 0 {
			m.WorkflowCostUSDTotal.Add(uint64(c.USDEstimate*usdToMicrocents + usdRoundHalf))
		}
		if c.Kind == StepImage {
			m.WorkflowImagesRenderedTotal.Add(1)
		}
	}

	if e.budgetUSD > 0 && wf.Cost != nil && wf.Cost.USDEstimate > e.budgetUSD {
		if m := e.getMetrics(); m != nil {
			m.WorkflowBudgetExceededTotal.Add(1)
		}
		return fmt.Errorf("%w: at step %s, spent $%.4f, limit $%.4f", ErrBudgetExceeded, c.StepID, wf.Cost.USDEstimate, e.budgetUSD)
	}
	return nil
}
