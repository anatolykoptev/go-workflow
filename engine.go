package workflow

import (
	"fmt"
	"log/slog"
	"time"
)

// ApprovalNotifier is called when a workflow needs user approval.
// Implementations should send a notification with approve/reject options.
type ApprovalNotifier func(wf *Workflow, step *Step)

// CompletionNotifier is called when a workflow reaches a terminal state
// (completed, failed, cancelled). Used to send result reports to the owner.
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
	bus                MessagePublisher
	approvalNotifier   ApprovalNotifier
	completionNotifier CompletionNotifier
	hooks              HookPublisher
	logger             *slog.Logger
	watchdogStop       chan struct{}
	scheduler          *Scheduler
	triggers           *TriggerService
	eventLog           *EventLog
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithMetrics sets the metrics instance for the engine.
// If not set, defaults to GlobalMetrics for backward compatibility.
func WithMetrics(m *Metrics) EngineOption {
	return func(e *Engine) { e.metrics = m }
}

// WithLogger sets the structured logger for the engine.
func WithLogger(l *slog.Logger) EngineOption {
	return func(e *Engine) { e.logger = l }
}

// WithHookPublisher sets the hook publisher for workflow lifecycle events.
func WithHookPublisher(h HookPublisher) EngineOption {
	return func(e *Engine) { e.hooks = h }
}

// WithMessagePublisher sets the message publisher for message steps.
func WithMessagePublisher(m MessagePublisher) EngineOption {
	return func(e *Engine) {
		e.bus = m
		e.executors[StepMessage] = NewMessageExecutor(m)
	}
}

// WithLLMProvider sets the LLM provider for LLM steps.
func WithLLMProvider(p LLMProvider) EngineOption {
	return func(e *Engine) {
		e.executors[StepLLM] = NewLLMExecutor(p, e.metrics)
	}
}

// WithToolRunner sets the tool runner for tool steps.
func WithToolRunner(t ToolRunner) EngineOption {
	return func(e *Engine) {
		e.executors[StepTool] = NewToolExecutor(t)
	}
}

// WithAgentRunner sets the agent runner for agent steps.
func WithAgentRunner(a AgentRunner) EngineOption {
	return func(e *Engine) {
		e.executors[StepAgent] = NewAgentExecutor(a, e.metrics)
	}
}

// WithA2ACaller sets the A2A caller for A2A steps.
func WithA2ACaller(c A2ACaller) EngineOption {
	return func(e *Engine) {
		e.executors[StepA2A] = NewA2AExecutor(c, e.metrics)
	}
}

// WithSkillResolver sets the skill resolver for LLM steps.
func WithSkillResolver(s SkillResolver) EngineOption {
	return func(e *Engine) {
		if llm, ok := e.executors[StepLLM].(*LLMExecutor); ok {
			llm.SetSkills(s)
		}
	}
}

// WithApprovalNotifier sets the callback for approval notifications.
func WithApprovalNotifier(fn ApprovalNotifier) EngineOption {
	return func(e *Engine) { e.approvalNotifier = fn }
}

// WithCompletionNotifier sets the callback for workflow completion reports.
func WithCompletionNotifier(fn CompletionNotifier) EngineOption {
	return func(e *Engine) { e.completionNotifier = fn }
}

// WithScheduler sets the scheduler for time-based triggers.
func WithScheduler(s *Scheduler) EngineOption {
	return func(e *Engine) { e.scheduler = s }
}

// WithTriggers sets the trigger service for event-based triggers.
func WithTriggers(ts *TriggerService) EngineOption {
	return func(e *Engine) { e.triggers = ts }
}

// WithEventLog sets the event log for structured execution tracing.
func WithEventLog(el *EventLog) EngineOption {
	return func(e *Engine) { e.eventLog = el }
}

// NewEngine creates a workflow engine with functional options.
// Only store is required. All other dependencies are optional.
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

	e.executors[StepWorkflow] = NewSubWorkflowExecutor(e)
	e.executors[StepForEach] = NewForEachExecutor(e)
	e.executors[StepBranchAll] = NewBranchAllExecutor(e)
	e.executors[StepSuspend] = NewSuspendExecutor()
	e.executors[StepNoop] = &NoopExecutor{}

	return e
}

// SetApprovalNotifier sets the callback for approval notifications.
func (e *Engine) SetApprovalNotifier(fn ApprovalNotifier) {
	e.approvalNotifier = fn
}

// SetAgentRunner registers an AgentExecutor for StepAgent steps.
// Called after engine creation to avoid circular dependency with agent package.
func (e *Engine) SetAgentRunner(runner AgentRunner) {
	e.executors[StepAgent] = NewAgentExecutor(runner, e.getMetrics())
}

// SetSkills sets the skill resolver for LLM steps that reference skills by name.
func (e *Engine) SetSkills(sr SkillResolver) {
	if llm, ok := e.executors[StepLLM].(*LLMExecutor); ok {
		llm.SetSkills(sr)
	}
}

// SetA2ACaller registers an A2AExecutor for StepA2A steps.
// Called after engine creation to avoid circular dependency with a2a package.
func (e *Engine) SetA2ACaller(caller A2ACaller) {
	e.executors[StepA2A] = NewA2AExecutor(caller, e.getMetrics())
}

// SetHooks sets the hook publisher for workflow lifecycle events.
func (e *Engine) SetHooks(h HookPublisher) {
	e.hooks = h
}

// SetCompletionNotifier sets the callback for workflow completion reports.
func (e *Engine) SetCompletionNotifier(fn CompletionNotifier) {
	e.completionNotifier = fn
}

// getMetrics returns the engine's metrics, falling back to GlobalMetrics if nil.
// This makes metrics safe when Engine is constructed as a struct literal in tests.
func (e *Engine) getMetrics() *Metrics {
	if e.metrics != nil {
		return e.metrics
	}
	return GlobalMetrics
}

// log returns the engine's logger, falling back to slog.Default() if nil.
// This makes logging safe when Engine is constructed as a struct literal in tests.
func (e *Engine) log() *slog.Logger {
	if e.logger != nil {
		return e.logger
	}
	return slog.Default()
}

// Store returns the underlying workflow store.
func (e *Engine) Store() *WorkflowStore {
	return e.store
}

// notifyCompletion calls the completion notifier if set and workflow is terminal.
func (e *Engine) notifyCompletion(workflowID string) {
	if e.completionNotifier == nil {
		return
	}
	w, ok := e.store.Load(workflowID)
	if !ok || !w.IsTerminal() {
		return
	}
	e.completionNotifier(w)
}

// fireHook fires a hook event if a publisher is set. Nil-safe.
func (e *Engine) fireHook(event string, data map[string]any) {
	if e.hooks != nil {
		e.hooks.Fire(event, data)
		e.getMetrics().HooksFired.Add(1)
	}
}

// emitEvent writes an event to the event log if configured. Nil-safe.
func (e *Engine) emitEvent(ev Event) {
	if e.eventLog != nil {
		_ = e.eventLog.Append(ev)
	}
}

// InjectStepsAndRewriteDeps atomically adds child steps after a parent step and rewrites dependencies.
// If newDepID is not empty, any step depending on afterStepID will be updated to depend on newDepID instead.
// Used by ForEach/BranchAll to expand meta-steps and ensure downstream steps wait for children.
func (e *Engine) InjectStepsAndRewriteDeps(workflowID string, steps []Step, afterStepID, newDepID string) error {
	return e.store.Modify(workflowID, func(w *Workflow) {
		w.Steps = insertSteps(w.Steps, steps, afterStepID)
		if newDepID != "" {
			rewriteDependencies(w.Steps, afterStepID, newDepID)
		}
		w.UpdatedAt = now()
	})
}

func insertSteps(existing []Step, newSteps []Step, afterID string) []Step {
	insertIdx := -1
	for i := range existing {
		if existing[i].ID == afterID {
			insertIdx = i + 1
			break
		}
	}
	if insertIdx < 0 {
		insertIdx = len(existing)
	}

	result := make([]Step, 0, len(existing)+len(newSteps))
	result = append(result, existing[:insertIdx]...)
	result = append(result, newSteps...)
	result = append(result, existing[insertIdx:]...)
	return result
}

func rewriteDependencies(steps []Step, oldDep, newDep string) {
	for i := range steps {
		for j, dep := range steps[i].DependsOn {
			if dep == oldDep {
				steps[i].DependsOn[j] = newDep
			}
		}
	}
}

func now() int64 { return time.Now().UnixMilli() }

// startWorkflow transitions a pending workflow to running state.
// Shared by Start and StartAsync to avoid duplication.
func (e *Engine) startWorkflow(workflowID string) (*Workflow, error) {
	w, ok := e.store.Load(workflowID)
	if !ok {
		return nil, fmt.Errorf("workflow %s not found", workflowID)
	}

	if w.State != StatePending {
		return nil, fmt.Errorf("workflow %s is %s, expected pending", workflowID, w.State)
	}

	// Idempotency check: reject if another active workflow has the same key
	if w.IdempotencyKey != "" {
		if existing := e.store.FindByIdempotencyKey(w.IdempotencyKey); existing != nil && existing.ID != workflowID {
			return nil, fmt.Errorf("duplicate idempotency key %q: active workflow %s", w.IdempotencyKey, existing.ID)
		}
	}

	if err := e.store.Modify(workflowID, func(w *Workflow) {
		w.State = StateRunning
		w.UpdatedAt = time.Now().UnixMilli()
	}); err != nil {
		return nil, err
	}

	e.getMetrics().WorkflowsCreated.Add(1)
	e.fireHook(EventWorkflowStarted, map[string]any{
		"workflow_id":   workflowID,
		"workflow_name": w.Name,
	})
	return w, nil
}

// findAllRunnable returns IDs of all steps that are pending and have all deps completed.
// Dead-lettered steps are treated as terminal (same as failed/skipped) for dependency resolution.
func (e *Engine) findAllRunnable(w *Workflow) []string {
	completed := make(map[string]bool)
	for _, s := range w.Steps {
		if s.State == StepCompleted || s.State == StepSkipped || s.State == StepDeadLettered {
			completed[s.ID] = true
		}
	}

	var runnable []string
	for _, s := range w.Steps {
		if s.State != StepPending {
			continue
		}

		allDepsMet := true
		for _, dep := range s.DependsOn {
			if !completed[dep] {
				allDepsMet = false
				break
			}
		}

		if allDepsMet {
			runnable = append(runnable, s.ID)
		}
	}

	return runnable
}

// applyStepFailure handles on_error routing for a failed step inside a store.Modify callback.
// Returns true if the error was handled (skip/branch), false if the workflow should fail.
func applyStepFailure(w *Workflow, stepID, errMsg string) bool {
	s := w.GetStep(stepID)
	if s == nil {
		return false
	}

	onError := s.GetOnError()
	switch {
	case onError == OnErrorSkip:
		s.State = StepSkipped
		s.Error = errMsg
		return true

	case onError != "" && onError != OnErrorFail:
		s.State = StepSkipped
		s.Error = errMsg
		w.Context[s.ID+"_error"] = errMsg
		w.Context[s.ID+"_failed"] = true
		if handler := w.GetStep(onError); handler != nil && handler.State == StepPending {
			handler.DependsOn = []string{}
		}
		return true

	default:
		s.State = StepFailed
		s.Error = errMsg
		w.State = StateFailed
		w.Error = fmt.Sprintf("step %s failed: %s", stepID, errMsg)
		return false
	}
}

// loadWorkflow loads a workflow or returns a formatted error.
func (e *Engine) loadWorkflow(workflowID string) (*Workflow, error) {
	w, ok := e.store.Load(workflowID)
	if !ok {
		return nil, fmt.Errorf("workflow %s not found", workflowID)
	}
	return w, nil
}
