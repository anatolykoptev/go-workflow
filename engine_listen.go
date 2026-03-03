package workflow

import "context"

// ListenForResults listens for step_done notifications and advances workflows.
// Blocks until ctx is cancelled. Run in a goroutine.
func (e *Engine) ListenForResults(ctx context.Context, listener *StepListener) {
	ch := listener.Listen(ctx)
	for event := range ch {
		e.log().Info("step done notification",
			"component", "workflow",
			"workflow", event.WorkflowID,
			"step", event.StepID,
		)
		// Re-advance the workflow DAG.
		go func(wfID string) {
			if _, err := e.Advance(ctx, wfID); err != nil {
				e.log().Error("advance after notification failed",
					"component", "workflow",
					"workflow", wfID,
					"error", err.Error(),
				)
			}
		}(event.WorkflowID)
	}
}
