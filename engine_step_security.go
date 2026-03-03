package workflow

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// checkSecurity validates budget, duration, and tool permission constraints.
// Returns a non-nil error if the step should be blocked.
func (e *Engine) checkSecurity(ctx context.Context, w *Workflow, step *Step, workflowID, stepID string) (context.Context, context.CancelFunc, error) {
	if err := e.checkBudgetAndDuration(w, workflowID); err != nil {
		return ctx, nil, err
	}

	if err := e.checkToolPermission(w, step, workflowID, stepID); err != nil {
		return ctx, nil, err
	}

	ctx, cancel := applyStepTimeout(ctx, w, step)
	return ctx, cancel, nil
}

func (e *Engine) checkBudgetAndDuration(w *Workflow, workflowID string) error {
	if w.Security == nil {
		return nil
	}

	if w.Security.MaxSteps > 0 && w.StepsExecuted >= w.Security.MaxSteps {
		return e.failWorkflow(workflowID, fmt.Errorf("workflow %s exceeded max steps budget (%d)", workflowID, w.Security.MaxSteps))
	}

	if w.Security.MaxDuration > 0 {
		elapsed := time.Duration(time.Now().UnixMilli()-w.CreatedAt) * time.Millisecond
		if elapsed > w.Security.MaxDuration {
			return e.failWorkflow(workflowID, fmt.Errorf("workflow %s exceeded max duration (%s)", workflowID, w.Security.MaxDuration))
		}
	}
	return nil
}

func (e *Engine) failWorkflow(workflowID string, err error) error {
	_ = e.store.Modify(workflowID, func(w *Workflow) {
		w.State = StateFailed
		w.Error = err.Error()
		w.UpdatedAt = time.Now().UnixMilli()
	})
	e.getMetrics().WorkflowsFailed.Add(1)
	return err
}

func (e *Engine) checkToolPermission(w *Workflow, step *Step, workflowID, stepID string) error {
	if step.Kind != StepTool {
		return nil
	}

	toolName, _ := step.Config["tool"].(string)
	if len(w.AllowedTools) > 0 && !slices.Contains(w.AllowedTools, toolName) {
		return e.failStepPermission(workflowID, stepID, toolName)
	}
	if w.Security != nil && !w.Security.IsToolAllowed(toolName) {
		return e.failStepPermission(workflowID, stepID, toolName)
	}
	return nil
}

func (e *Engine) failStepPermission(workflowID, stepID, toolName string) error {
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
	e.getMetrics().WorkflowsFailed.Add(1)
	return permErr
}

func applyStepTimeout(ctx context.Context, w *Workflow, step *Step) (context.Context, context.CancelFunc) {
	stepTimeout := step.GetTimeoutMS()
	if stepTimeout <= 0 && w.Security != nil && w.Security.MaxStepDuration > 0 {
		stepTimeout = w.Security.MaxStepDuration.Milliseconds()
	}
	if stepTimeout > 0 {
		return context.WithTimeout(ctx, time.Duration(stepTimeout)*time.Millisecond)
	}
	return ctx, nil
}
