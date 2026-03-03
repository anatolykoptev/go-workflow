package workflow

import (
	"context"
	"fmt"
	"time"
)

// GracefulShutdown pauses all workflows and stops the watchdog.
func (e *Engine) GracefulShutdown(timeout time.Duration) int {
	paused := e.PauseAll()
	e.StopWatchdog()

	return paused
}

// PauseAll pauses all running workflows. Used for graceful shutdown.
func (e *Engine) PauseAll() int {
	running := e.store.List(StateRunning)
	waiting := e.store.List(StateWaitingApproval)

	paused := 0
	for _, w := range append(running, waiting...) {
		_ = e.store.Modify(w.ID, func(w *Workflow) {
			w.State = StatePaused
			w.UpdatedAt = now()
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
// and resumes them. Running steps are reset to pending.
func (e *Engine) RecoverAll(ctx context.Context) []string {
	running := e.store.List(StateRunning)
	var recovered []string
	for _, w := range running {
		e.resetRunningSteps(w.ID)
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
func (e *Engine) ResumeAll(ctx context.Context) []string {
	paused := e.store.List(StatePaused)
	var resumed []string
	for _, w := range paused {
		_ = e.store.Modify(w.ID, func(w *Workflow) {
			w.State = StateRunning
			e.resetStepsToState(w, StepRunning, StepPending)
			w.UpdatedAt = now()
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

// resetRunningSteps resets any steps stuck in StepRunning back to StepPending.
func (e *Engine) resetRunningSteps(workflowID string) {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		e.resetStepsToState(w, StepRunning, StepPending)
		w.UpdatedAt = now()
	})
}

// resetStepsToState changes steps from fromState to toState, resetting their StartedAt.
func (e *Engine) resetStepsToState(w *Workflow, from, to StepState) {
	for i := range w.Steps {
		if w.Steps[i].State == from {
			w.Steps[i].State = to
			w.Steps[i].StartedAt = 0
			e.log().Info("reset step",
				"component", "workflow",
				"workflow", w.ID,
				"step", w.Steps[i].ID,
			)
		}
	}
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
		return e.rejectApproval(workflowID, w)
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
		if w.CurrentStep != "" {
			w.InterruptBefore = removeString(w.InterruptBefore, w.CurrentStep)
			w.InterruptAfter = removeString(w.InterruptAfter, w.CurrentStep)
		}
		w.State = StateRunning
		w.UpdatedAt = time.Now().UnixMilli()
	})
}

func (e *Engine) rejectApproval(workflowID string, w *Workflow) error {
	e.getMetrics().WorkflowsCancelled.Add(1)
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

// Cancel cancels a running or paused workflow.
func (e *Engine) Cancel(workflowID string) error {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}

	if w.IsTerminal() {
		return fmt.Errorf("workflow %s is already %s", workflowID, w.State)
	}

	e.getMetrics().WorkflowsCancelled.Add(1)
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
