package workflow

import (
	"context"
	"sync"
	"time"
)

// Start sets a pending workflow to running and begins synchronous execution.
// Blocks until the workflow reaches a terminal or paused state.
func (e *Engine) Start(ctx context.Context, workflowID string) error {
	if _, err := e.startWorkflow(workflowID); err != nil {
		return err
	}
	return e.RunToCompletion(ctx, workflowID)
}

// StartAsync sets a pending workflow to running and begins execution in a background goroutine.
// Returns immediately after starting. The CompletionNotifier is called when the workflow finishes.
func (e *Engine) StartAsync(ctx context.Context, workflowID string) error {
	if _, err := e.startWorkflow(workflowID); err != nil {
		return err
	}

	// Detach from caller's context — the workflow must outlive the HTTP request
	// or Telegram handler that triggered it.
	go func() {
		if err := e.RunToCompletion(context.Background(), workflowID); err != nil {
			e.log().Error("async execution failed",
				"component", "workflow",
				"workflow", workflowID,
				"error", err.Error(),
			)
		}
		e.notifyCompletion(workflowID)
	}()

	return nil
}

// ResumeAsync resumes a running workflow in a background goroutine.
// Used after approval or any state change that allows continued execution.
// The caller's context is intentionally ignored — the workflow must outlive
// the HTTP handler or MCP tool call that triggered the resume.
func (e *Engine) ResumeAsync(_ context.Context, workflowID string) {
	go func() {
		if err := e.RunToCompletion(context.Background(), workflowID); err != nil {
			e.log().Error("async resume failed",
				"component", "workflow",
				"workflow", workflowID,
				"error", err.Error(),
			)
		}
		e.notifyCompletion(workflowID)
	}()
}

// RunToCompletion runs Advance in a loop until the workflow stops (completed, failed, approval, paused).
//
// When Advance returns an error AND the workflow has entered StateFailed but
// still has runnable AlwaysRun steps, we keep iterating so cleanup steps drain.
// The original error is preserved and returned once draining finishes.
func (e *Engine) RunToCompletion(ctx context.Context, workflowID string) error {
	var firstErr error
	for {
		select {
		case <-ctx.Done():
			if firstErr != nil {
				return firstErr
			}
			return ctx.Err()
		default:
		}

		advanced, err := e.Advance(ctx, workflowID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Drain any remaining always_run steps before returning.
			if e.hasRunnableAlwaysRun(workflowID) {
				continue
			}
			return firstErr
		}
		if !advanced {
			return firstErr
		}
	}
}

// hasRunnableAlwaysRun reports whether the workflow has any pending AlwaysRun
// steps whose dependencies have all reached a terminal state (so they could
// be scheduled by a subsequent Advance call).
func (e *Engine) hasRunnableAlwaysRun(workflowID string) bool {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return false
	}
	for _, sid := range e.findAllRunnable(w) {
		if s := w.GetStep(sid); s != nil && s.AlwaysRun {
			return true
		}
	}
	return false
}

// Advance finds all runnable steps (all deps completed) and executes them.
// Independent steps run in parallel. Returns true if any step was executed.
//
// Special-case: when the workflow is in StateFailed, Advance still drains any
// pending AlwaysRun steps whose deps have reached a terminal state. Cancelled,
// Completed, WaitingApproval, and Paused short-circuit unconditionally.
func (e *Engine) Advance(ctx context.Context, workflowID string) (bool, error) {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return false, err
	}

	if w.State == StateCompleted || w.State == StateCancelled ||
		w.State == StateWaitingApproval || w.State == StatePaused {
		return false, nil
	}

	runnableSteps := e.findAllRunnable(w)
	if len(runnableSteps) == 0 {
		// Nothing left to do: if we're not already failed, try to mark complete.
		// If we ARE failed, leave the state — failure cause must be preserved.
		if w.State != StateFailed {
			e.tryMarkCompleted(w, workflowID)
		}
		return false, nil
	}

	d := e.getDispatcher()
	if len(runnableSteps) == 1 {
		step := w.GetStep(runnableSteps[0])
		return true, d.Dispatch(ctx, workflowID, runnableSteps[0], step.Kind)
	}

	kinds := make([]StepKind, len(runnableSteps))
	for i, sid := range runnableSteps {
		kinds[i] = w.GetStep(sid).Kind
	}
	return true, d.DispatchBatch(ctx, workflowID, runnableSteps, kinds)
}

// tryMarkCompleted checks if all steps are terminal and marks the workflow completed.
func (e *Engine) tryMarkCompleted(w *Workflow, workflowID string) {
	for _, s := range w.Steps {
		if s.State != StepCompleted && s.State != StepSkipped && s.State != StepDeadLettered {
			return
		}
	}
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		w.State = StateCompleted
		w.CurrentStep = ""
		w.UpdatedAt = time.Now().UnixMilli()
	})
	e.getMetrics().WorkflowsCompleted.Add(1)
	e.log().Info("workflow completed",
		"component", "workflow",
		"workflow", workflowID,
		"duration", time.Now().UnixMilli()-w.CreatedAt,
	)
	e.fireHook(EventWorkflowCompleted, map[string]any{
		"workflow_id":   workflowID,
		"workflow_name": w.Name,
		"duration_ms":   time.Now().UnixMilli() - w.CreatedAt,
	})
}

// runParallel executes multiple independent steps concurrently.
func (e *Engine) runParallel(ctx context.Context, workflowID string, steps []string) error {
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup

	for _, stepID := range steps {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			if err := e.RunStep(ctx, workflowID, sid); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(stepID)
	}
	wg.Wait()
	return firstErr
}
