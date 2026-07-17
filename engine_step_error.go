package workflow

import (
	"fmt"
	"slices"
	"time"
)

// handleInterrupt checks interrupt_before for the step and pauses the workflow if triggered.
// Returns true if the workflow was paused (caller should return nil).
func (e *Engine) handleInterrupt(w *Workflow, workflowID, stepID string) bool {
	if !slices.Contains(w.InterruptBefore, stepID) {
		return false
	}
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepPending
			s.StartedAt = 0
		}
		w.State = StateWaitingApproval
		w.UpdatedAt = time.Now().UnixMilli()
	})
	e.getMetrics().ApprovalsPending.Add(1)
	e.log().Info("interrupt_before",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
	)
	e.fireHook(EventWorkflowApprovalNeeded, map[string]any{
		"workflow_id": workflowID,
		"step_id":     stepID,
		"reason":      "interrupt_before",
	})
	return true
}

// handleSuspend pauses the workflow until a deadline. The watchdog resumes it.
func (e *Engine) handleSuspend(workflowID, stepID string, step *Step, stepContext map[string]any, endedAt int64) error {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepCompleted
			s.Result = step.Result
			s.EndedAt = endedAt
		}
		mergeContext(w, stepContext)
		w.StepsExecuted++
		w.State = StatePaused
		w.UpdatedAt = time.Now().UnixMilli()
	})
	e.log().Info("workflow suspended",
		"component", "workflow",
		"workflow", workflowID,
		"step", stepID,
	)
	return nil
}

// handleApprovalRequired transitions a step to waiting-for-approval state.
//
// If the approval step sets Config["approval_timeout_ms"] (a relative duration
// in ms — e.g. 86400000 for 24h), an absolute deadline is computed as
// now + timeout and stored in Context[stepID+"_approval_deadline_ms"]. The
// watchdog's cancelExpiredApprovals() auto-cancels the workflow once that
// deadline passes (issue #25). This is the ONLY entry into
// StateWaitingApproval that wires a timeout — handleInterrupt and the
// interrupt_after checkpoint in completeStep set StateWaitingApproval too but
// for a different (non-approval) pause mechanism and are deliberately NOT
// wired (see issue #25 out-of-scope note). The deadline key uses a distinct
// "_approval_deadline_ms" suffix so it never collides with SuspendExecutor's
// "_suspend_until_ms" keys.
func (e *Engine) handleApprovalRequired(workflowID, stepID string, step *Step, endedAt int64) error {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		if s := w.GetStep(stepID); s != nil {
			s.State = StepPending
			s.EndedAt = endedAt
		}
		w.State = StateWaitingApproval
		w.UpdatedAt = time.Now().UnixMilli()
		// Optional per-gate approval timeout (issue #25). Go JSON-decodes
		// numbers as float64 — same unmarshal pattern SuspendExecutor uses
		// for its suspend_until_ms config field. A positive duration
		// computes an absolute deadline relative to when the approval-wait
		// begins (a workflow author configures this ahead of time, before
		// knowing when any given run will reach the gate).
		if timeoutMS, ok := step.Config["approval_timeout_ms"].(float64); ok && timeoutMS > 0 {
			w.Context[stepID+"_approval_deadline_ms"] = time.Now().UnixMilli() + int64(timeoutMS)
		}
	})
	e.getMetrics().ApprovalsPending.Add(1)
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

// handleStepError processes step failure: retries, skip, error-branch, dead letter, or hard fail.
func (e *Engine) handleStepError(workflowID, stepID string, step *Step, w *Workflow, execErr error, endedAt int64) error {
	maxRetries := step.GetRetryMax()
	errMsg := execErr.Error()

	if didRetry, attempt := e.tryRetry(workflowID, stepID, step, errMsg, endedAt); didRetry {
		e.getMetrics().StepsRetried.Add(1)
		// Apply jitter to avoid thundering herd on correlated retries.
		// retryAfterFloor honors upstream Retry-After hints (e.g. 429 responses).
		delay := calculateBackoffWithJitter(step.GetRetryDelayMS(), attempt, step.GetBackoffMultiplier(), step.GetMaxDelayMS())
		delay = retryAfterFloor(delay, execErr)
		e.log().Info("step retrying",
			"component", "workflow",
			"workflow", workflowID,
			"step", stepID,
			"attempt", attempt,
			"max", maxRetries,
			"delay_ms", delay,
		)
		time.Sleep(time.Duration(delay) * time.Millisecond)
		return nil
	}

	// Retries exhausted or conditional retry declined — check on_error routing
	onError := step.GetOnError()

	handled := false
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		s := w.GetStep(stepID)
		if s == nil {
			return
		}
		s.EndedAt = endedAt

		// Dead letter if retries exhausted and on_error == "fail"
		if maxRetries > 0 && s.Retries >= maxRetries && onError == OnErrorFail {
			s.State = StepDeadLettered
			s.Error = errMsg
			w.State = StateFailed
			w.Error = fmt.Sprintf("step %s dead-lettered: %s", stepID, errMsg)
			e.getMetrics().StepsDeadLettered.Add(1)
			return
		}

		handled = applyStepFailure(w, stepID, errMsg)
		w.UpdatedAt = time.Now().UnixMilli()
	})

	if handled {
		e.getMetrics().StepsSkipped.Add(1)
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
	e.getMetrics().WorkflowsFailed.Add(1)
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
	e.emitEvent(Event{
		Type: EventStepFailed, WorkflowID: workflowID,
		StepID: stepID, StepKind: string(step.Kind),
		DurationMS: endedAt - step.StartedAt, Error: errMsg,
	})
	e.fireHook(EventWorkflowFailed, map[string]any{
		"workflow_id":   workflowID,
		"workflow_name": w.Name,
		"error":         errMsg,
	})
	return execErr
}

// tryRetry attempts to schedule a retry for a failed step.
// Returns (true, attempt) if retried, (false, 0) if retries exhausted or declined.
func (e *Engine) tryRetry(workflowID, stepID string, step *Step, errMsg string, endedAt int64) (bool, int) {
	maxRetries := step.GetRetryMax()
	retryOn := step.GetRetryOn()
	skipOn := step.GetSkipOn()

	didRetry := false
	var retryAttempt int
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		s := w.GetStep(stepID)
		if s == nil {
			return
		}

		shouldRetry := s.Retries < maxRetries

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

	return didRetry, retryAttempt
}
