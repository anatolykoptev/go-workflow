package workflow

import (
	"context"
	"fmt"
)

// SubWorkflowRunner is the interface for running sub-workflows (satisfied by Engine).
type SubWorkflowRunner interface {
	Store() *WorkflowStore
	Start(ctx context.Context, workflowID string) error
	RunToCompletion(ctx context.Context, workflowID string) error
}

// SubWorkflowExecutor runs another workflow as a child step.
// Config: {"workflow_id": "child-id"}
// The child workflow must already exist in the store. The parent step
// blocks until the child completes. Child results are available in the
// parent context under the step ID.
type SubWorkflowExecutor struct {
	runner SubWorkflowRunner
}

func NewSubWorkflowExecutor(runner SubWorkflowRunner) *SubWorkflowExecutor {
	return &SubWorkflowExecutor{runner: runner}
}

func (e *SubWorkflowExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	childID, _ := step.Config["workflow_id"].(string)
	if childID == "" {
		return fmt.Errorf("step %s: missing 'workflow_id' in config", step.ID)
	}

	child, ok := e.runner.Store().Load(childID)
	if !ok {
		return fmt.Errorf("step %s: child workflow %s not found", step.ID, childID)
	}

	// Start child if pending
	switch child.State {
	case StatePending:
		if err := e.runner.Start(ctx, childID); err != nil {
			return fmt.Errorf("step %s: start child %s: %w", step.ID, childID, err)
		}
	case StateRunning, StatePaused:
		// Resume running child
		if err := e.runner.RunToCompletion(ctx, childID); err != nil {
			return fmt.Errorf("step %s: resume child %s: %w", step.ID, childID, err)
		}
	}

	// Check child final state
	child, ok = e.runner.Store().Load(childID)
	if !ok {
		return fmt.Errorf("step %s: child workflow %s disappeared", step.ID, childID)
	}
	if child.State == StateCompleted {
		step.Result = child.Context
		wf.Context[step.ID] = child.Context
		return nil
	}

	if child.State == StateWaitingApproval {
		return fmt.Errorf("step %s: child workflow %s is waiting for approval", step.ID, childID)
	}

	return fmt.Errorf("step %s: child workflow %s ended with state %s: %s", step.ID, childID, child.State, child.Error)
}
