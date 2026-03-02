package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	defaultStallThreshold = 15 * time.Minute // watchdog: stall detection threshold
	defaultRetryMaxAge    = 30 * time.Minute // watchdog: auto-retry age window
)

// --- Stall detection (addresses n8n random workflow stops) ---

// StalledWorkflow describes a workflow with a step that has been running too long.
type StalledWorkflow struct {
	WorkflowID   string        `json:"workflow_id"`
	WorkflowName string        `json:"workflow_name"`
	StepID       string        `json:"step_id"`
	StepKind     StepKind      `json:"step_kind"`
	RunningFor   time.Duration `json:"running_for_ms"`
}

// DetectStalled finds workflows with steps that have been running longer than the threshold.
// Default threshold: 15 minutes. Returns stalled workflow info for monitoring/alerting.
func (e *Engine) DetectStalled(threshold time.Duration) []StalledWorkflow {
	if threshold <= 0 {
		threshold = 15 * time.Minute
	}

	now := time.Now().UnixMilli()
	var stalled []StalledWorkflow

	running := e.store.List(StateRunning)
	for _, wf := range running {
		for _, step := range wf.Steps {
			if step.State != StepRunning || step.StartedAt == 0 {
				continue
			}
			elapsed := time.Duration(now-step.StartedAt) * time.Millisecond
			if elapsed > threshold {
				stalled = append(stalled, StalledWorkflow{
					WorkflowID:   wf.ID,
					WorkflowName: wf.Name,
					StepID:       step.ID,
					StepKind:     step.Kind,
					RunningFor:   elapsed,
				})
			}
		}
	}

	return stalled
}

// RecoverStalled attempts to recover stalled workflows by failing the hung step
// and letting the engine's error handling (retry/on_error) take over.
// Returns the number of workflows recovered.
func (e *Engine) RecoverStalled(threshold time.Duration) int {
	stalled := e.DetectStalled(threshold)
	recovered := 0

	for _, sw := range stalled {
		_ = e.store.Modify(sw.WorkflowID, func(w *Workflow) {
			if s := w.GetStep(sw.StepID); s != nil && s.State == StepRunning {
				errMsg := fmt.Sprintf("step stalled for %s, auto-recovered", sw.RunningFor)
				s.EndedAt = time.Now().UnixMilli()
				applyStepFailure(w, sw.StepID, errMsg)
				w.UpdatedAt = time.Now().UnixMilli()
			}
		})
		recovered++
		e.log().Warn("recovered stalled workflow",
			"component", "workflow",
			"workflow", sw.WorkflowID,
			"step", sw.StepID,
			"running_for", sw.RunningFor.String(),
		)
	}

	return recovered
}

// --- Self-healing: Auto-retry transient errors ---

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

		err := e.store.Modify(wf.ID, func(w *Workflow) {
			s := w.GetStep(failedStepID)
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
			continue
		}

		e.getMetrics().StepsRetried.Add(1)
		e.log().Info("auto-retrying transient failure",
			"component", "workflow",
			"workflow", wf.ID,
			"step", failedStepID,
			"error", wf.Error,
		)

		go func(id string) {
			e.ResumeAsync(context.Background(), id)
		}(wf.ID)
		retried++
	}

	return retried
}

// --- Watchdog goroutine ---

// StartWatchdog begins a background goroutine that periodically:
// 1. Recovers stalled workflows (running > 15min)
// 2. Auto-retries recently failed workflows with transient errors
// Call StopWatchdog to shut it down gracefully.
func (e *Engine) StartWatchdog(interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	e.watchdogStop = make(chan struct{})

	if e.scheduler != nil {
		if err := e.scheduler.Start(); err != nil {
			e.log().Error("scheduler start failed", "error", err.Error())
		} else {
			e.log().Info("scheduler started", "component", "workflow")
		}
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		e.log().Info("watchdog started",
			"component", "workflow",
			"interval", interval.String(),
		)

		for {
			select {
			case <-e.watchdogStop:
				e.log().Info("watchdog stopped", "component", "workflow")
				return
			case <-ticker.C:
				// 1. Recover stalled steps
				recovered := e.RecoverStalled(defaultStallThreshold)
				if recovered > 0 {
					e.log().Warn("watchdog recovered stalled",
						"component", "workflow",
						"count", recovered,
					)
					e.getMetrics().HooksFired.Add(1) // reuse counter for watchdog actions
				}

				// 2. Auto-retry transient failures (within last 30 min)
				retried := e.AutoRetryFailed(defaultRetryMaxAge)
				if retried > 0 {
					e.log().Info("watchdog auto-retried transient failures",
						"component", "workflow",
						"count", retried,
					)
				}

				// 3. Auto-resume suspended workflows past deadline
				e.resumeSuspended()
			}
		}
	}()
}

// StopWatchdog stops the background watchdog goroutine.
func (e *Engine) StopWatchdog() {
	if e.watchdogStop != nil {
		close(e.watchdogStop)
	}
	if e.scheduler != nil {
		e.scheduler.Stop()
	}
}

// resumeSuspended finds paused workflows with expired suspend deadlines and resumes them.
func (e *Engine) resumeSuspended() {
	paused := e.store.List(StatePaused)
	nowMS := time.Now().UnixMilli()

	for _, w := range paused {
		var deadline int64
		for k, v := range w.Context {
			if strings.HasSuffix(k, "_suspend_until_ms") {
				if d, ok := v.(float64); ok && int64(d) > 0 {
					deadline = int64(d)
					break
				}
				if d, ok := v.(int64); ok && d > 0 {
					deadline = d
					break
				}
			}
		}

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
