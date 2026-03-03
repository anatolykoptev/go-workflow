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

	// Execute
	execErr := executor.Execute(ctx, step, w)
	endedAt := time.Now().UnixMilli()

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
