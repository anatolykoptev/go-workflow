package workflow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// RunStep executes a single step, updating state and persisting.
// All state mutations go through store.Modify to be concurrent-safe.
func (e *Engine) RunStep(ctx context.Context, workflowID, stepID string) error {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}

	step := w.GetStep(stepID)
	if step == nil {
		return fmt.Errorf("step %s not found in workflow %s", stepID, workflowID)
	}

	executor, ok := e.executors[step.Kind]
	if !ok {
		return fmt.Errorf("no executor for step kind %q", step.Kind)
	}

	// Security checks: budget, duration, tool permissions, per-step timeout
	ctx, cancel, secErr := e.checkSecurity(ctx, w, step, workflowID, stepID)
	if cancel != nil {
		defer cancel()
	}
	if secErr != nil {
		return secErr
	}

	// Mark step running
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepRunning
			s.StartedAt = time.Now().UnixMilli()
		}
		w.CurrentStep = stepID
		w.UpdatedAt = time.Now().UnixMilli()
	})

	// interrupt_before: pause before executing
	if e.handleInterrupt(w, workflowID, stepID) {
		return nil
	}

	e.log().Info("step started",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
		"kind", string(step.Kind),
	)
	e.fireHook(EventWorkflowStepStarted, map[string]any{
		"workflow_id": workflowID,
		"step_id":     stepID,
		"step_kind":   string(step.Kind),
	})
	e.emitEvent(Event{
		Type: EventStepStarted, WorkflowID: workflowID,
		StepID: stepID, StepKind: string(step.Kind),
	})

	// Cache lookup BEFORE span open — a hit short-circuits execution and
	// records a dedicated cache_hit span instead of a full executor span.
	cacheKey, cacheable := e.stepCacheKey(w, step)
	if cacheable {
		if entry, ok, err := e.stepCacheGet(ctx, cacheKey); err == nil && ok {
			return e.applyCacheHit(ctx, workflowID, stepID, w, step, cacheKey, entry)
		}
		if m := e.getMetrics(); m != nil {
			m.StepCacheMisses.Add(1)
		}
	}

	// Open OTel span around the executor call. Span carries the trace context
	// downstream so child operations (HTTP, MCP) join the same trace.
	spanCtx, span := e.startStepSpan(ctx, w, step)
	execStart := time.Now()
	// QPS rate limit (opt-in): check before calling the executor.
	if e.rateLimits != nil {
		provider := stepProviderKey(step)
		if limitErr := e.rateLimits.check(provider); limitErr != nil {
			finishStepSpan(span, step, 0, false, limitErr)
			return limitErr
		}
	}
	execErr := executor.Execute(spanCtx, step, w)
	endedAt := time.Now().UnixMilli()
	durationMS := time.Since(execStart).Milliseconds()
	finishStepSpan(span, step, durationMS, false, execErr)

	if execErr == nil && cacheable {
		_ = e.stepCachePut(ctx, cacheKey, w, step)
	}

	e.getMetrics().StepsExecuted.Add(1)

	stepResult := step.Result
	stepContext := maps.Clone(w.Context)

	if errors.Is(execErr, errApprovalRequired) {
		return e.handleApprovalRequired(workflowID, stepID, step, endedAt)
	}
	if errors.Is(execErr, errSuspendRequested) {
		return e.handleSuspend(workflowID, stepID, step, stepContext, endedAt)
	}
	if execErr != nil {
		return e.handleStepError(workflowID, stepID, step, w, execErr, endedAt)
	}

	return e.completeStep(workflowID, stepID, w, step, stepResult, stepContext, endedAt)
}

// completeStep marks a step as successfully completed and checks interrupt_after.
func (e *Engine) completeStep(workflowID, stepID string, w *Workflow, step *Step, result any, stepContext map[string]any, endedAt int64) error {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepCompleted
			s.Result = result
			s.EndedAt = endedAt
		}
		mergeContext(w, stepContext)
		w.StepsExecuted++
		w.UpdatedAt = time.Now().UnixMilli()
	})

	// interrupt_after: pause after completing
	if slices.Contains(w.InterruptAfter, stepID) {
		_ = e.store.Modify(workflowID, func(w *Workflow) {
			w.State = StateWaitingApproval
			w.UpdatedAt = time.Now().UnixMilli()
		})
		e.getMetrics().ApprovalsPending.Add(1)
		e.log().Info("interrupt_after",
			"component", "workflow",
			"workflow", workflowID,
			"step", stepID,
		)
		e.fireHook(EventWorkflowApprovalNeeded, map[string]any{
			"workflow_id": workflowID,
			"step_id":     stepID,
			"reason":      "interrupt_after",
		})
		return nil
	}

	e.log().Info("step completed",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
		"duration", endedAt-step.StartedAt,
	)
	e.fireHook(EventWorkflowStepCompleted, map[string]any{
		"workflow_id": workflowID,
		"step_id":     stepID,
		"step_kind":   string(step.Kind),
		"duration_ms": endedAt - step.StartedAt,
	})
	e.emitEvent(Event{
		Type: EventStepFinished, WorkflowID: workflowID,
		StepID: stepID, StepKind: string(step.Kind),
		DurationMS: endedAt - step.StartedAt,
	})

	return nil
}
