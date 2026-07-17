package workflow

import (
	"context"
	"fmt"
	"time"
)

const approvalResult = "approved"

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
//
// stepID optionally targets a specific approval gate by id. When empty, the
// gate is resolved via BlockingStep() (the single-gate auto-resolution from
// #23 — backward-compatible). When set, the named step must exist, be a
// pending approval gate (Kind==StepApproval && State==StepPending), AND be
// reachable — every step in its DependsOn must already be in a
// terminal-satisfied state (completed/skipped/dead-lettered, the same set
// findAllRunnable treats as "completed"), else an error is returned and the
// workflow is left untouched. This prevents out-of-order completion of a
// downstream gate whose upstream hasn't run yet (issue #24 reachability guard).
//
// Rejection (approved==false) always cancels the WHOLE workflow regardless of
// stepID — step_id scopes which gate is validated/targeted, not what gets
// rejected; a valid target's rejection is workflow-global by design.
func (e *Engine) HandleApproval(workflowID string, approved bool, stepID string) error {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}

	if w.State != StateWaitingApproval {
		return fmt.Errorf("workflow %s is %s, not waiting_approval", workflowID, w.State)
	}

	// Explicit step_id targeting (issue #24): validate the named gate is a
	// pending approval step BEFORE any mutation, so a bad target returns a
	// clear error and leaves the workflow untouched. When step_id is empty,
	// resolution defers to BlockingStep() inside the Modify callback below
	// (backward-compatible with the pre-#24 single-gate auto-resolution).
	if stepID != "" {
		step := w.GetStep(stepID)
		if step == nil {
			return fmt.Errorf("workflow %s step %q not found", workflowID, stepID)
		}
		if step.Kind != StepApproval || step.State != StepPending {
			return fmt.Errorf("workflow %s step %q is not a pending approval step", workflowID, stepID)
		}
		// Reachability guard (issue #24): a pending approval gate that
		// hasn't been reached yet (its DependsOn aren't all
		// terminal-satisfied) must not be completable out of order —
		// findAllRunnable would then never schedule it again, silently
		// bypassing the human approval that was supposed to happen there.
		if dep, ok := stepDepsSatisfied(w, step); !ok {
			return fmt.Errorf("workflow %s step %q is not reachable yet: dependency %q is not completed", workflowID, stepID, dep)
		}
	}

	if !approved {
		return e.rejectApproval(workflowID, w)
	}

	return e.store.Modify(workflowID, func(w *Workflow) {
		// Target selection: when stepID is set, resolve the NAMED gate
		// explicitly (issue #24 — addressable approval targeting). When
		// empty, defer to the authoritative CurrentStep via BlockingStep,
		// so wf_status's pending_approval and this resolver can never
		// disagree on which gate is being approved — see #23.
		// BlockingStep returns nil for an active interrupt_before/
		// interrupt_after pause point (a non-approval checkpoint — see #23
		// round 4), so only a real pending approval gate reaches here; the
		// Kind==StepApproval && State==StepPending guard is kept as a
		// defensive check (also guards the stepID path against a race that
		// completes the named gate between the pre-Modify validation and
		// here). The stepDepsSatisfied re-check extends that TOCTOU defense
		// to reachability: if a race made the named gate's upstream regress
		// to non-terminal between the pre-Modify validation and here, do
		// NOT complete the gate (the workflow still transitions to Running
		// and the real blocking gate gets scheduled normally). When the
		// resolved gate is nil (interrupt pause or nothing to resolve),
		// only the interrupt-list cleanup and State->StateRunning
		// transition below run.
		var gate *Step
		if stepID != "" {
			gate = w.GetStep(stepID)
		} else {
			gate = w.BlockingStep()
		}
		if gate != nil && gate.Kind == StepApproval && gate.State == StepPending {
			if _, ok := stepDepsSatisfied(w, gate); ok {
				gate.State = StepCompleted
				gate.Result = approvalResult
				gate.EndedAt = time.Now().UnixMilli()
				w.Context[gate.ID] = approvalResult
				// Hygiene (issue #25): clear the per-gate approval timeout
				// deadline so long-lived workflows with many approval cycles
				// don't accumulate stale _approval_deadline_ms keys in Context
				// unbounded. Not correctness-critical — cancelExpiredApprovals
				// scopes strictly to BlockingStep()'s current gate, so a stale
				// key from a resolved gate can never trigger a wrong cancel —
				// but cheap cleanup alongside the gate-resolution write.
				delete(w.Context, gate.ID+"_approval_deadline_ms")
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

// HandleApprovalWithData resumes a workflow with structured data from the approver.
// Data is stored in wf.Context[stepID] for downstream steps to consume via $steps.{id}.result.
// If data is nil, falls back to approvalResult string (same as HandleApproval).
//
// stepID optionally targets a specific approval gate by id — see HandleApproval.
func (e *Engine) HandleApprovalWithData(workflowID string, approved bool, data map[string]any, stepID string) error {
	if !approved {
		return e.HandleApproval(workflowID, false, stepID)
	}

	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}
	if w.State != StateWaitingApproval {
		return fmt.Errorf("workflow %s is %s, not waiting_approval", workflowID, w.State)
	}

	// Explicit step_id targeting (issue #24): validate the named gate is a
	// pending approval step BEFORE any mutation — see HandleApproval.
	if stepID != "" {
		step := w.GetStep(stepID)
		if step == nil {
			return fmt.Errorf("workflow %s step %q not found", workflowID, stepID)
		}
		if step.Kind != StepApproval || step.State != StepPending {
			return fmt.Errorf("workflow %s step %q is not a pending approval step", workflowID, stepID)
		}
		// Reachability guard (issue #24) — see HandleApproval.
		if dep, ok := stepDepsSatisfied(w, step); !ok {
			return fmt.Errorf("workflow %s step %q is not reachable yet: dependency %q is not completed", workflowID, stepID, dep)
		}
	}

	return e.store.Modify(workflowID, func(w *Workflow) {
		// Target selection: when stepID is set, resolve the NAMED gate
		// explicitly (issue #24); otherwise defer to BlockingStep — same
		// derivation as HandleApproval/wf_status (#23). BlockingStep returns
		// nil for an active interrupt_before/interrupt_after pause point (a
		// non-approval checkpoint — see #23 round 4), so only a real pending
		// approval gate reaches here; the Kind==StepApproval &&
		// State==StepPending guard is kept as a defensive check (also guards
		// the stepID path against a race that completes the named gate
		// between the pre-Modify validation and here). The stepDepsSatisfied
		// re-check extends that TOCTOU defense to reachability — see
		// HandleApproval. When the resolved gate is nil, only the
		// interrupt-list cleanup and State->StateRunning transition below
		// run, leaving the paused step to execute for real.
		var gate *Step
		if stepID != "" {
			gate = w.GetStep(stepID)
		} else {
			gate = w.BlockingStep()
		}
		if gate != nil && gate.Kind == StepApproval && gate.State == StepPending {
			if _, ok := stepDepsSatisfied(w, gate); ok {
				gate.State = StepCompleted
				gate.EndedAt = time.Now().UnixMilli()
				if data != nil {
					gate.Result = data
					w.Context[gate.ID] = data
				} else {
					gate.Result = approvalResult
					w.Context[gate.ID] = approvalResult
				}
				// Hygiene (issue #25): clear the per-gate approval timeout
				// deadline so long-lived workflows with many approval cycles
				// don't accumulate stale _approval_deadline_ms keys in Context
				// unbounded. Not correctness-critical — cancelExpiredApprovals
				// scopes strictly to BlockingStep()'s current gate, so a stale
				// key from a resolved gate can never trigger a wrong cancel —
				// but cheap cleanup alongside the gate-resolution write.
				delete(w.Context, gate.ID+"_approval_deadline_ms")
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

// Reopen transitions a cancelled workflow back to waiting_approval, provided
// it is safely resumable — i.e. its CurrentStep points at a pending approval
// step (the gate it was waiting on when cancelled; Cancel only touches
// workflow-level State/Error, so that step is still StepPending). This is the
// inverse of Cancel for the "stop parking this, needs a human decision"
// cleanup pattern: once the human resolves the blocking issue, the caller can
// Reopen and then proceed with a normal HandleApproval/HandleApprovalWithData
// on the SAME workflow.
//
// It does NOT touch step states — the pending approval step is already
// correctly StepPending, ready for HandleApproval right after. Step selection
// uses the workflow's own CurrentStep field (the authoritative "which step am
// I actually blocked on" signal) rather than counting pending approval steps:
// every not-yet-reached approval step defaults to StepPending from creation,
// so a count-based scan cannot distinguish "the one it's blocked on" from
// "future placeholder pendings" and would refuse any workflow cancelled before
// its final approval gate. If CurrentStep is empty, or points at a step that is
// missing / not a pending approval step, there is nothing to reopen INTO and an
// error is returned.
func (e *Engine) Reopen(workflowID string) error {
	w, err := e.loadWorkflow(workflowID)
	if err != nil {
		return err
	}

	if w.State != StateCancelled {
		return fmt.Errorf("workflow %s is %s, not cancelled", workflowID, w.State)
	}

	if w.CurrentStep == "" {
		return fmt.Errorf("workflow %s has no current step, nothing to reopen into", workflowID)
	}

	step := w.GetStep(w.CurrentStep)
	if step == nil {
		return fmt.Errorf("workflow %s current step %q not found in steps", workflowID, w.CurrentStep)
	}

	if step.Kind != StepApproval || step.State != StepPending {
		return fmt.Errorf("workflow %s current step %q is not a pending approval step, nothing to reopen into", workflowID, w.CurrentStep)
	}

	if err := e.store.Modify(workflowID, func(w *Workflow) {
		w.State = StateWaitingApproval
		w.Error = ""
		w.UpdatedAt = time.Now().UnixMilli()
		// Clear the stale per-gate approval timeout deadline (issue #25 bug
		// fix): if this workflow was auto-cancelled by cancelExpiredApprovals,
		// its Context still holds the now-PAST gate.ID+"_approval_deadline_ms"
		// key (Cancel never deletes it; only HandleApproval's normal-resolve
		// path did). Left in place, the very next watchdog tick would find the
		// SAME gate with the SAME already-expired deadline and cancel the
		// freshly-reopened workflow again before the human can act — a
		// deterministic re-cancel loop. Reopen intentionally does NOT recompute
		// nor reapply a fresh timeout from the step's original config: the
		// original auto-timeout already served its purpose (it got the human's
		// attention), so we simply clear the stale one, matching how a normal
		// HandleApproval resolve already clears it.
		delete(w.Context, step.ID+"_approval_deadline_ms")
	}); err != nil {
		return err
	}

	// Every other entry into WaitingApproval fires this event (interrupt_after,
	// interrupt_before, handleApprovalRequired) — a subscriber (approval-
	// notification/UI driver) needs to learn a reopened workflow is waiting
	// again, same as it would for a fresh approval gate.
	e.fireHook(EventWorkflowApprovalNeeded, map[string]any{
		"workflow_id": workflowID,
		"step_id":     step.ID,
		"reason":      "reopened",
	})
	return nil
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
