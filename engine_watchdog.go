package workflow

import (
	"fmt"
	"time"
)

const (
	defaultStallThreshold = 15 * time.Minute // watchdog: stall detection threshold
	defaultRetryMaxAge    = 30 * time.Minute // watchdog: auto-retry age window
)

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
				e.runWatchdogCycle()
			}
		}
	}()
}

func (e *Engine) runWatchdogCycle() {
	recovered := e.RecoverStalled(defaultStallThreshold)
	if recovered > 0 {
		e.log().Warn("watchdog recovered stalled",
			"component", "workflow",
			"count", recovered,
		)
		e.getMetrics().HooksFired.Add(1)
	}

	retried := e.AutoRetryFailed(defaultRetryMaxAge)
	if retried > 0 {
		e.log().Info("watchdog auto-retried transient failures",
			"component", "workflow",
			"count", retried,
		)
	}

	e.resumeSuspended()
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
