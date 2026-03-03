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
