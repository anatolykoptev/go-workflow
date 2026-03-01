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
	// Load a snapshot for read-only checks and execution
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

	// Security: check step budget
	if w.Security != nil && w.Security.MaxSteps > 0 && w.StepsExecuted >= w.Security.MaxSteps {
		budgetErr := fmt.Errorf("workflow %s exceeded max steps budget (%d)", workflowID, w.Security.MaxSteps)
		_ = e.store.Modify(workflowID, func(w *Workflow) {
			w.State = StateFailed
			w.Error = budgetErr.Error()
			w.UpdatedAt = time.Now().UnixMilli()
		})
		GlobalMetrics.WorkflowsFailed.Add(1)
		return budgetErr
	}

	// Security: check workflow max duration
	if w.Security != nil && w.Security.MaxDuration > 0 {
		elapsed := time.Duration(time.Now().UnixMilli()-w.CreatedAt) * time.Millisecond
		if elapsed > w.Security.MaxDuration {
			timeoutErr := fmt.Errorf("workflow %s exceeded max duration (%s)", workflowID, w.Security.MaxDuration)
			_ = e.store.Modify(workflowID, func(w *Workflow) {
				w.State = StateFailed
				w.Error = timeoutErr.Error()
				w.UpdatedAt = time.Now().UnixMilli()
			})
			GlobalMetrics.WorkflowsFailed.Add(1)
			return timeoutErr
		}
	}

	// Security: check tool permissions (legacy AllowedTools + SecurityPolicy)
	if step.Kind == StepTool {
		toolName, _ := step.Config["tool"].(string)
		allowed := true

		// Legacy AllowedTools check
		if len(w.AllowedTools) > 0 && !slices.Contains(w.AllowedTools, toolName) {
			allowed = false
		}

		// SecurityPolicy check (more granular)
		if w.Security != nil && !w.Security.IsToolAllowed(toolName) {
			allowed = false
		}

		if !allowed {
			permErr := fmt.Errorf("tool %q not permitted in workflow %s", toolName, workflowID)
			_ = e.store.Modify(workflowID, func(w *Workflow) {
				if s := w.GetStep(stepID); s != nil {
					s.State = StepFailed
					s.Error = permErr.Error()
					s.EndedAt = time.Now().UnixMilli()
				}
				w.State = StateFailed
				w.Error = fmt.Sprintf("step %s failed: %s", stepID, permErr.Error())
				w.UpdatedAt = time.Now().UnixMilli()
			})
			GlobalMetrics.WorkflowsFailed.Add(1)
			return permErr
		}
	}

	// Per-step timeout: step config > SecurityPolicy > none
	stepTimeout := step.GetTimeoutMS()
	if stepTimeout <= 0 && w.Security != nil && w.Security.MaxStepDuration > 0 {
		stepTimeout = w.Security.MaxStepDuration.Milliseconds()
	}
	if stepTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(stepTimeout)*time.Millisecond)
		defer cancel()
	}

	// Mark step running (atomic)
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepRunning
			s.StartedAt = time.Now().UnixMilli()
		}
		w.CurrentStep = stepID
		w.UpdatedAt = time.Now().UnixMilli()
	})

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

	// Execute (on snapshot — executor writes to step.Result and wf.Context)
	execErr := executor.Execute(ctx, step, w)
	endedAt := time.Now().UnixMilli()

	GlobalMetrics.StepsExecuted.Add(1)

	// Capture executor results to merge back atomically
	stepResult := step.Result
	stepContext := maps.Clone(w.Context)

	if errors.Is(execErr, errApprovalRequired) {
		return e.handleApprovalRequired(workflowID, stepID, step, endedAt)
	}

	if execErr != nil {
		return e.handleStepError(workflowID, stepID, step, w, execErr, endedAt)
	}

	// Success — merge results atomically
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepCompleted
			s.Result = stepResult
			s.EndedAt = endedAt
		}
		// Merge context from executor
		for k, v := range stepContext {
			w.Context[k] = v
		}
		w.StepsExecuted++
		w.UpdatedAt = time.Now().UnixMilli()
	})

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

	return nil
}

// handleApprovalRequired transitions a step to waiting-for-approval state.
func (e *Engine) handleApprovalRequired(workflowID, stepID string, step *Step, endedAt int64) error {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepPending
			s.EndedAt = endedAt
		}
		w.State = StateWaitingApproval
		w.UpdatedAt = time.Now().UnixMilli()
	})
	GlobalMetrics.ApprovalsPending.Add(1)
	e.log().Info("waiting for approval",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
	)

	e.fireHook(EventWorkflowApprovalNeeded, map[string]any{
		"workflow_id": workflowID,
		"step_id":     stepID,
		"step_kind":   string(step.Kind),
	})

	if e.approvalNotifier != nil {
		wSnap, _ := e.store.Load(workflowID)
		if sSnap := wSnap.GetStep(stepID); sSnap != nil {
			e.approvalNotifier(wSnap, sSnap)
		}
	}
	return nil
}

// handleStepError processes step failure: retries (with backoff + conditional), skip, error-branch, dead letter, or hard fail.
func (e *Engine) handleStepError(workflowID, stepID string, step *Step, w *Workflow, execErr error, endedAt int64) error {
	maxRetries := step.GetRetryMax()
	baseDelayMS := step.GetRetryDelayMS()
	backoffMult := step.GetBackoffMultiplier()
	maxDelayMS := step.GetMaxDelayMS()
	retryOn := step.GetRetryOn()
	skipOn := step.GetSkipOn()
	errMsg := execErr.Error()

	didRetry := false
	var retryAttempt int
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		s := w.GetStep(stepID)
		if s == nil {
			return
		}

		shouldRetry := s.Retries < maxRetries

		// Conditional retry: skip_on takes precedence, then retry_on
		if shouldRetry && len(skipOn) > 0 {
			shouldRetry = !matchesAnyPattern(errMsg, skipOn)
		}
		if shouldRetry && len(retryOn) > 0 {
			shouldRetry = matchesAnyPattern(errMsg, retryOn)
		}

		if shouldRetry {
			s.Retries++
			retryAttempt = s.Retries
			s.State = StepPending
			s.Error = fmt.Sprintf("attempt %d/%d: %s", s.Retries, maxRetries, errMsg)
			s.EndedAt = endedAt
			didRetry = true
		}
		w.UpdatedAt = time.Now().UnixMilli()
	})

	if didRetry {
		GlobalMetrics.StepsRetried.Add(1)
		delay := calculateBackoff(baseDelayMS, retryAttempt, backoffMult, maxDelayMS)
		e.log().Info("step retrying",
			"component", "workflow",
			"workflow", workflowID,
			"step", stepID,
			"attempt", retryAttempt,
			"max", maxRetries,
			"delay_ms", delay,
		)
		time.Sleep(time.Duration(delay) * time.Millisecond)
		return nil
	}

	// Retries exhausted or conditional retry declined — check on_error routing
	onError := step.GetOnError()

	// Apply on_error routing (skip / branch / fail / dead_letter)
	handled := false
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		s := w.GetStep(stepID)
		if s == nil {
			return
		}
		s.EndedAt = endedAt

		// If retries were configured and exhausted, and on_error == "fail" → dead letter
		if maxRetries > 0 && s.Retries >= maxRetries && onError == OnErrorFail {
			s.State = StepDeadLettered
			s.Error = errMsg
			w.State = StateFailed
			w.Error = fmt.Sprintf("step %s dead-lettered: %s", stepID, errMsg)
			GlobalMetrics.StepsDeadLettered.Add(1)
			return
		}

		handled = applyStepFailure(w, stepID, errMsg)
		w.UpdatedAt = time.Now().UnixMilli()
	})

	if handled {
		GlobalMetrics.StepsSkipped.Add(1)
		e.log().Info("step error handled",
			"component", "workflow",
			"workflow", workflowID,
			"step", stepID,
			"on_error", onError,
			"error", errMsg,
		)
		return nil
	}

	// Hard failure (or dead letter)
	GlobalMetrics.WorkflowsFailed.Add(1)
	e.log().Error("step failed",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
		"error", errMsg,
		"duration", endedAt-step.StartedAt,
	)
	e.fireHook(EventWorkflowStepFailed, map[string]any{
		"workflow_id": workflowID,
		"step_id":     stepID,
		"step_kind":   string(step.Kind),
		"error":       errMsg,
		"duration_ms": endedAt - step.StartedAt,
	})
	e.fireHook(EventWorkflowFailed, map[string]any{
		"workflow_id":   workflowID,
		"workflow_name": w.Name,
		"error":         errMsg,
	})
	return execErr
}
