package workflow

import (
	"context"
	"fmt"
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
func (e *Engine) ResumeAsync(ctx context.Context, workflowID string) {
	go func() {
		if err := e.RunToCompletion(ctx, workflowID); err != nil {
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
func (e *Engine) RunToCompletion(ctx context.Context, workflowID string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		advanced, err := e.Advance(ctx, workflowID)
		if err != nil {
			return err
		}
		if !advanced {
			return nil
		}
	}
}

// Advance finds all runnable steps (all deps completed) and executes them.
// Independent steps run in parallel. Returns true if any step was executed.
func (e *Engine) Advance(ctx context.Context, workflowID string) (bool, error) {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return false, err
	}

	if w.IsTerminal() || w.State == StateWaitingApproval || w.State == StatePaused {
		return false, nil
	}

	runnableSteps := e.findAllRunnable(w)
	if len(runnableSteps) == 0 {
		// Check if all steps completed → workflow completed
		allDone := true
		for _, s := range w.Steps {
			if s.State != StepCompleted && s.State != StepSkipped && s.State != StepDeadLettered {
				allDone = false
				break
			}
		}
		if allDone {
			_ = e.store.Modify(workflowID, func(w *Workflow) {
				w.State = StateCompleted
				w.CurrentStep = ""
				w.UpdatedAt = time.Now().UnixMilli()
			})
			GlobalMetrics.WorkflowsCompleted.Add(1)
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
		return false, nil
	}

	// Run single step directly, multiple in parallel
	if len(runnableSteps) == 1 {
		err := e.RunStep(ctx, workflowID, runnableSteps[0])
		return true, err
	}

	// Parallel execution for independent steps
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup

	for _, stepID := range runnableSteps {
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

	return true, firstErr
}

// HandleApproval resumes or rejects a workflow waiting for approval.
func (e *Engine) HandleApproval(workflowID string, approved bool) error {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}

	if w.State != StateWaitingApproval {
		return fmt.Errorf("workflow %s is %s, not waiting_approval", workflowID, w.State)
	}

	if !approved {
		GlobalMetrics.WorkflowsCancelled.Add(1)
		err := e.store.Modify(workflowID, func(w *Workflow) {
			w.State = StateCancelled
			w.Error = "approval rejected"
			w.UpdatedAt = time.Now().UnixMilli()
		})
		if err == nil {
			e.fireHook(EventWorkflowCancelled, map[string]any{
				"workflow_id":   workflowID,
				"workflow_name": w.Name,
				"reason":        "approval_rejected",
			})
			e.notifyCompletion(workflowID)
		}
		return err
	}

	return e.store.Modify(workflowID, func(w *Workflow) {
		for i := range w.Steps {
			s := &w.Steps[i]
			if s.Kind == StepApproval && s.State == StepPending {
				s.State = StepCompleted
				s.Result = "approved"
				s.EndedAt = time.Now().UnixMilli()
				w.Context[s.ID] = "approved"
				break
			}
		}

		w.State = StateRunning
		w.UpdatedAt = time.Now().UnixMilli()
	})
}

// Cancel cancels a running or paused workflow.
func (e *Engine) Cancel(workflowID string) error {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}

	if w.IsTerminal() {
		return fmt.Errorf("workflow %s is already %s", workflowID, w.State)
	}

	GlobalMetrics.WorkflowsCancelled.Add(1)
	err = e.store.Modify(workflowID, func(w *Workflow) {
		w.State = StateCancelled
		w.Error = "cancelled by user"
		w.UpdatedAt = time.Now().UnixMilli()
	})
	if err == nil {
		e.fireHook(EventWorkflowCancelled, map[string]any{
			"workflow_id":   workflowID,
			"workflow_name": w.Name,
		})
		e.notifyCompletion(workflowID)
	}
	return err
}

// PauseAll pauses all running workflows. Used for graceful shutdown.
// Returns the number of workflows paused.
func (e *Engine) PauseAll() int {
	running := e.store.List(StateRunning)
	waiting := e.store.List(StateWaitingApproval)

	paused := 0
	for _, w := range append(running, waiting...) {
		_ = e.store.Modify(w.ID, func(w *Workflow) {
			w.State = StatePaused
			w.UpdatedAt = time.Now().UnixMilli()
		})
		paused++
		e.log().Info("paused for shutdown",
			"component", "workflow",
			"workflow", w.ID,
		)
	}
	return paused
}

// RecoverAll finds workflows stuck in StateRunning at startup (sign of a crash)
// and resumes them. Running steps are reset to pending. Returns recovered IDs.
func (e *Engine) RecoverAll(ctx context.Context) []string {
	running := e.store.List(StateRunning)
	var recovered []string
	for _, w := range running {
		_ = e.store.Modify(w.ID, func(w *Workflow) {
			for i := range w.Steps {
				if w.Steps[i].State == StepRunning {
					w.Steps[i].State = StepPending
					w.Steps[i].StartedAt = 0
					e.log().Info("reset crashed step",
						"component", "workflow",
						"workflow", w.ID,
						"step", w.Steps[i].ID,
					)
				}
			}
			w.UpdatedAt = time.Now().UnixMilli()
		})
		recovered = append(recovered, w.ID)
		e.log().Info("recovered after crash",
			"component", "workflow",
			"workflow", w.ID,
		)
		go func(id string) {
			if err := e.RunToCompletion(ctx, id); err != nil {
				e.log().Error("recovery execution failed",
					"component", "workflow",
					"workflow", id,
					"error", err.Error(),
				)
			}
			e.notifyCompletion(id)
		}(w.ID)
	}
	return recovered
}

// ResumeAll resumes all paused workflows. Used after restart.
// Returns the IDs of resumed workflows.
func (e *Engine) ResumeAll(ctx context.Context) []string {
	paused := e.store.List(StatePaused)
	var resumed []string
	for _, w := range paused {
		_ = e.store.Modify(w.ID, func(w *Workflow) {
			w.State = StateRunning
			// Reset steps that were running when paused — they need to be
			// re-executed since no goroutine is processing them anymore.
			for i := range w.Steps {
				if w.Steps[i].State == StepRunning {
					w.Steps[i].State = StepPending
					w.Steps[i].StartedAt = 0
					e.log().Info("reset interrupted step",
						"component", "workflow",
						"workflow", w.ID,
						"step", w.Steps[i].ID,
					)
				}
			}
			w.UpdatedAt = time.Now().UnixMilli()
		})
		resumed = append(resumed, w.ID)
		e.log().Info("resumed after restart",
			"component", "workflow",
			"workflow", w.ID,
		)
		go func(id string) {
			if err := e.RunToCompletion(ctx, id); err != nil {
				e.log().Error("resume failed",
					"component", "workflow",
					"workflow", id,
					"error", err.Error(),
				)
			}
		}(w.ID)
	}
	return resumed
}
