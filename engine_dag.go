package workflow

import (
	"fmt"
	"time"
)

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

	// Idempotency check
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
// Dead-lettered steps are treated as terminal for dependency resolution.
//
// AlwaysRun steps relax the rule slightly: a failed dep also counts as terminal
// for them (so cleanup can proceed after upstream failure). Pending deps still
// block — never schedule cleanup with unmet inputs.
func (e *Engine) findAllRunnable(w *Workflow) []string {
	// Standard "completed" set (ok / skipped / dead-lettered).
	completed := make(map[string]bool)
	// Extended "terminal" set used only for AlwaysRun steps (adds failed).
	terminal := make(map[string]bool)
	for _, s := range w.Steps {
		switch s.State {
		case StepCompleted, StepSkipped, StepDeadLettered:
			completed[s.ID] = true
			terminal[s.ID] = true
		case StepFailed:
			terminal[s.ID] = true
		}
	}

	var runnable []string
	for _, s := range w.Steps {
		if s.State != StepPending {
			continue
		}

		// If the workflow has already failed and this step is NOT marked
		// always_run, do not schedule it. (Plain pending steps must not
		// surprise-run after a hard failure — preserve original semantics.)
		if !s.AlwaysRun && w.State == StateFailed {
			continue
		}

		pool := completed
		if s.AlwaysRun {
			pool = terminal
		}

		allDepsMet := true
		for _, dep := range s.DependsOn {
			if !pool[dep] {
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

// stepDepsSatisfied reports whether every step in s.DependsOn is in a
// terminal-satisfied state — the same set findAllRunnable treats as
// "completed" above (StepCompleted, StepSkipped, StepDeadLettered). Returns
// the first unsatisfied dependency id (or "" when all are met); a dependency
// referenced in DependsOn but absent from w.Steps, or one still
// pending/running/failed, counts as unsatisfied. Used by the step_id
// targeting path in HandleApproval/HandleApprovalWithData to refuse
// completing an approval gate whose upstream hasn't run yet — see issue #24
// reachability guard.
func stepDepsSatisfied(w *Workflow, s *Step) (missing string, ok bool) {
	for _, dep := range s.DependsOn {
		d := w.GetStep(dep)
		if d == nil {
			return dep, false
		}
		switch d.State {
		case StepCompleted, StepSkipped, StepDeadLettered:
			continue
		default:
			return dep, false
		}
	}
	return "", true
}

// loadWorkflow loads a workflow or returns a formatted error.
func (e *Engine) loadWorkflow(workflowID string) (*Workflow, error) {
	w, ok := e.store.Load(workflowID)
	if !ok {
		return nil, fmt.Errorf("workflow %s not found", workflowID)
	}
	return w, nil
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
	case onError == OnErrorSkip || onError == OnErrorContinue:
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
		// Preserve the first failure cause; an always_run cleanup step's failure
		// must not mask the original error. The workflow-level err return in
		// RunToCompletion already preserves the first error; mirror it on the
		// persisted field.
		if w.State != StateFailed || w.Error == "" {
			w.State = StateFailed
			w.Error = fmt.Sprintf("step %s failed: %s", stepID, errMsg)
		}
		return false
	}
}

// InjectStepsAndRewriteDeps atomically adds child steps after a parent step and rewrites dependencies.
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
