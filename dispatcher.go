package workflow

import "context"

// WithDispatcher sets a custom step dispatcher on the engine.
func WithDispatcher(d StepDispatcher) EngineOption {
	return func(e *Engine) { e.dispatcher = d }
}

// StepDispatcher decides how steps are executed.
// LocalDispatcher runs them in-process; PostgresDispatcher will enqueue to a step_queue table.
type StepDispatcher interface {
	Dispatch(ctx context.Context, workflowID, stepID string, kind StepKind) error
	DispatchBatch(ctx context.Context, workflowID string, stepIDs []string, kinds []StepKind) error
}

// LocalDispatcher runs steps directly on the embedded engine (in-process).
// This preserves the original single-node behavior.
type LocalDispatcher struct {
	engine *Engine
}

// NewLocalDispatcher creates a dispatcher that calls engine.RunStep directly.
func NewLocalDispatcher(engine *Engine) *LocalDispatcher {
	return &LocalDispatcher{engine: engine}
}

// Dispatch executes a single step synchronously via the engine.
func (d *LocalDispatcher) Dispatch(ctx context.Context, workflowID, stepID string, _ StepKind) error {
	return d.engine.RunStep(ctx, workflowID, stepID)
}

// DispatchBatch executes multiple steps concurrently via engine.runParallel.
// Returns the first error encountered.
func (d *LocalDispatcher) DispatchBatch(ctx context.Context, workflowID string, stepIDs []string, _ []StepKind) error {
	return d.engine.runParallel(ctx, workflowID, stepIDs)
}

// getDispatcher returns the configured dispatcher, falling back to a LocalDispatcher.
// This nil-safe accessor ensures backward compatibility with engines created without NewEngine.
func (e *Engine) getDispatcher() StepDispatcher {
	if e.dispatcher != nil {
		return e.dispatcher
	}
	return NewLocalDispatcher(e)
}
