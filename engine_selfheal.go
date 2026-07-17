package workflow

import (
	"context"
	"strings"
	"time"
)

// transientPatterns are error substrings that indicate a retryable failure.
var transientPatterns = []string{
	"capacity exhausted",
	"rate limit",
	"circuit open", // circuit breaker open — back off until half-open
	"429",
	"503",
	"timeout",
	"timed out",
	"connection refused",
	"connection reset",
	"temporary failure",
	"empty model response",
	"missing thought",
	"thought_signature",
	"UNAVAILABLE",
	"RESOURCE_EXHAUSTED",
}

// IsTransientError checks if an error message matches known transient patterns.
func IsTransientError(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, p := range transientPatterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// findRetryableFailedStep returns the ID of the first StepFailed step in the workflow.
// Returns "" if no retryable step is found (e.g., all failures are dead-lettered).
func findRetryableFailedStep(wf *Workflow) string {
	for _, s := range wf.Steps {
		if s.State == StepFailed {
			return s.ID
		}
	}
	return "" // no retryable step (may be dead-lettered only)
}

// AutoRetryFailed checks recently failed workflows for transient errors and retries them.
// Returns the number of workflows retried. Only retries workflows that failed within maxAge.
func (e *Engine) AutoRetryFailed(maxAge time.Duration) int {
	failed := e.store.List(StateFailed)
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	retried := 0

	for _, wf := range failed {
		if wf.UpdatedAt < cutoff {
			continue // too old
		}
		if !IsTransientError(wf.Error) {
			continue // permanent error
		}

		failedStepID := findRetryableFailedStep(wf)
		if failedStepID == "" {
			continue
		}

		if err := e.retryFailedStep(wf, failedStepID); err != nil {
			continue
		}
		retried++
	}

	return retried
}

func (e *Engine) retryFailedStep(wf *Workflow, stepID string) error {
	err := e.store.Modify(wf.ID, func(w *Workflow) {
		s := w.GetStep(stepID)
		if s == nil || s.State != StepFailed {
			return
		}
		s.State = StepPending
		s.Error = ""
		s.StartedAt = 0
		s.EndedAt = 0
		w.State = StateRunning
		w.Error = ""
		w.UpdatedAt = time.Now().UnixMilli()
	})
	if err != nil {
		return err
	}

	e.getMetrics().StepsRetried.Add(1)
	e.log().Info("auto-retrying transient failure",
		"component", "workflow",
		"workflow", wf.ID,
		"step", stepID,
		"error", wf.Error,
	)

	go func(id string) {
		e.ResumeAsync(context.Background(), id)
	}(wf.ID)
	return nil
}

// resumeSuspended finds paused workflows with expired suspend deadlines and resumes them.
func (e *Engine) resumeSuspended() {
	paused := e.store.List(StatePaused)
	nowMS := time.Now().UnixMilli()

	for _, w := range paused {
		deadline := findSuspendDeadline(w)
		if deadline > 0 && nowMS >= deadline {
			_ = e.store.Modify(w.ID, func(w *Workflow) {
				w.State = StateRunning
				w.UpdatedAt = nowMS
			})
			e.log().Info("watchdog resumed suspended workflow",
				"component", "workflow",
				"workflow", w.ID,
			)
			e.ResumeAsync(context.Background(), w.ID)
		}
	}
}

func findSuspendDeadline(w *Workflow) int64 {
	for k, v := range w.Context {
		if strings.HasSuffix(k, "_suspend_until_ms") {
			if d, ok := v.(float64); ok && int64(d) > 0 {
				return int64(d)
			}
			if d, ok := v.(int64); ok && d > 0 {
				return d
			}
		}
	}
	return 0
}

// cancelExpiredApprovals auto-cancels workflows whose approval gate has passed
// its optional per-gate timeout (issue #25). It is the approval-side analogue
// of resumeSuspended: where resumeSuspended resumes StatePaused workflows past
// their suspend deadline, this cancels StateWaitingApproval workflows past
// their approval deadline.
//
// The deadline check is scoped STRICTLY to BlockingStep()'s current gate — it
// looks up Context[gate.ID+"_approval_deadline_ms"] for the one step the
// workflow is actually blocked on right now, never scanning all Context keys.
// This is deliberate: a long-lived workflow that has passed through multiple
// approval gates (possibly via Reopen cycles) can accumulate STALE
// _approval_deadline_ms keys from PREVIOUSLY-resolved gates (resolving a gate
// does not clear its deadline key — same characteristic findSuspendDeadline's
// suspend keys already have). A raw Context-key scan would see a stale expired
// key from an already-resolved, unrelated past gate and wrongly cancel a
// workflow now waiting on a different, non-expired gate. Scoping to
// BlockingStep().ID avoids this by construction — it can only ever look at the
// deadline for the ACTUAL step the workflow is blocked on.
//
// If BlockingStep() returns nil (the workflow is in StateWaitingApproval but
// paused on an interrupt_before/interrupt_after checkpoint — a non-approval
// pause mechanism per #23 round 4), there is no approval gate to time out and
// the workflow is skipped entirely.
func (e *Engine) cancelExpiredApprovals() {
	waiting := e.store.List(StateWaitingApproval)
	nowMS := time.Now().UnixMilli()

	for _, w := range waiting {
		// Cheap pre-filter on the stale snapshot — avoids a Modify call for
		// every waiting workflow on every tick. The REAL decision (and the
		// only state mutation) happens inside store.Modify below, re-checked
		// against fresh state to close the TOCTOU window against a concurrent
		// HandleApproval: if a human approves in the window between this List()
		// snapshot and the Modify below, the workflow has already moved to
		// StateRunning, its deadline key was already deleted, and ResumeAsync
		// may already be executing downstream steps. The fresh re-check inside
		// the closure sees the state has moved on and refuses to cancel,
		// instead of silently undoing a legitimate approval (issue #25 bug
		// fix). This duplicates just enough of Cancel's state-transition logic
		// to make it safely re-checkable inside one atomic closure; the public
		// Cancel method itself is unchanged.
		gate := w.BlockingStep()
		if gate == nil {
			continue // interrupt checkpoint, not an approval gate — out of scope (#25)
		}
		deadline := findApprovalDeadline(w, gate.ID)
		if deadline <= 0 || nowMS < deadline {
			continue
		}

		var cancelled bool
		var cancelledStepID string
		var overdue time.Duration
		err := e.store.Modify(w.ID, func(fresh *Workflow) {
			if fresh.State != StateWaitingApproval {
				return // resolved/reopened/cancelled by something else in the meantime
			}
			freshGate := fresh.BlockingStep()
			if freshGate == nil {
				return
			}
			freshDeadline := findApprovalDeadline(fresh, freshGate.ID)
			freshNow := time.Now().UnixMilli()
			if freshDeadline <= 0 || freshNow < freshDeadline {
				return // deadline cleared/changed/not-yet-expired by the time we actually mutate
			}
			fresh.State = StateCancelled
			fresh.Error = "approval timeout exceeded"
			fresh.UpdatedAt = freshNow
			cancelled = true
			cancelledStepID = freshGate.ID
			overdue = time.Duration(freshNow-freshDeadline) * time.Millisecond
		})
		if err != nil {
			e.log().Warn("watchdog failed to cancel expired approval",
				"component", "workflow",
				"workflow", w.ID,
				"step", gate.ID,
				"error", err.Error(),
			)
			continue
		}
		if !cancelled {
			continue // the fresh re-check inside Modify found it no longer applicable
		}
		e.getMetrics().WorkflowsCancelled.Add(1)
		e.fireHook(EventWorkflowCancelled, map[string]any{
			"workflow_id":   w.ID,
			"workflow_name": w.Name,
			"reason":        "approval_timeout",
		})
		e.notifyCompletion(w.ID)
		e.log().Warn("watchdog cancelled expired approval",
			"component", "workflow",
			"workflow", w.ID,
			"step", cancelledStepID,
			"overdue", overdue.String(),
		)
	}
}

// findApprovalDeadline reads the per-gate approval timeout deadline for the
// given step ID from the workflow Context, handling both int64 and float64
// stored-value cases (the same defensive pattern findSuspendDeadline uses).
// Returns 0 when no deadline is set for that gate (no timeout configured).
func findApprovalDeadline(w *Workflow, stepID string) int64 {
	v, ok := w.Context[stepID+"_approval_deadline_ms"]
	if !ok {
		return 0
	}
	if d, ok := v.(float64); ok && int64(d) > 0 {
		return int64(d)
	}
	if d, ok := v.(int64); ok && d > 0 {
		return d
	}
	return 0
}
