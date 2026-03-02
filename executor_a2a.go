package workflow

import (
	"context"
	"fmt"
	"time"
)

// A2ACaller is the interface for calling remote A2A agents.
// Satisfied by *a2a.ClientManager (which has Call(ctx, agentID, message)).
type A2ACaller interface {
	Call(ctx context.Context, agentID, message string) (string, error)
}

// A2AExecutor delegates a step to a remote A2A agent.
type A2AExecutor struct {
	caller  A2ACaller
	metrics *Metrics
}

func NewA2AExecutor(caller A2ACaller, metrics *Metrics) *A2AExecutor {
	return &A2AExecutor{caller: caller, metrics: metrics}
}

func (e *A2AExecutor) Execute(ctx context.Context, step *Step, wf *Workflow) error {
	agentID, _ := step.Config["agent_id"].(string)
	if agentID == "" {
		return fmt.Errorf("step %s: missing 'agent_id' in config", step.ID)
	}

	message, _ := step.Config["message"].(string)
	if message == "" {
		return fmt.Errorf("step %s: missing 'message' in config", step.ID)
	}

	message = resolvePromptRefs(message, wf)

	// Apply per-call timeout override if configured
	if timeoutSec, ok := step.Config["timeout_seconds"].(float64); ok && timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec*float64(time.Second)))
		defer cancel()
	}

	result, err := e.caller.Call(ctx, agentID, message)
	if err != nil {
		e.metrics.A2AStepsFailed.Add(1)
		return fmt.Errorf("a2a %s: %w", agentID, err)
	}

	e.metrics.A2AStepsExecuted.Add(1)
	step.Result = result
	wf.Context[step.ID] = result
	return nil
}
