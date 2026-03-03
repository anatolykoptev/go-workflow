package workflow

import (
	"context"
	"fmt"
	"io"
)

// StepEnqueuer enqueues steps for distributed execution.
// Implemented by store.StepQueue.
type StepEnqueuer interface {
	Enqueue(item QueueItem) error
	io.Closer
}

// PostgresDispatcher implements StepDispatcher by enqueuing steps
// to the step_queue table for distributed worker execution.
type PostgresDispatcher struct {
	queue   StepEnqueuer
	closers []io.Closer
}

// NewPostgresDispatcher creates a dispatcher from a StepEnqueuer.
// Additional closers (e.g. ConcurrencyLimiter) can be passed for lifecycle management.
func NewPostgresDispatcher(queue StepEnqueuer, closers ...io.Closer) *PostgresDispatcher {
	return &PostgresDispatcher{
		queue:   queue,
		closers: closers,
	}
}

// Dispatch enqueues a single step to the step_queue table.
func (d *PostgresDispatcher) Dispatch(_ context.Context, workflowID, stepID string, kind StepKind) error {
	return d.queue.Enqueue(QueueItem{
		WorkflowID: workflowID,
		StepID:     stepID,
		StepKind:   string(kind),
	})
}

// DispatchBatch enqueues multiple steps to the step_queue table.
func (d *PostgresDispatcher) DispatchBatch(
	_ context.Context, workflowID string, stepIDs []string, kinds []StepKind,
) error {
	for i, stepID := range stepIDs {
		item := QueueItem{
			WorkflowID: workflowID,
			StepID:     stepID,
			StepKind:   string(kinds[i]),
		}
		if err := d.queue.Enqueue(item); err != nil {
			return fmt.Errorf("enqueue step %d/%d (%s): %w", i+1, len(stepIDs), stepID, err)
		}
	}
	return nil
}

// Close releases the queue and any additional closers.
func (d *PostgresDispatcher) Close() error {
	var firstErr error

	if err := d.queue.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("close queue: %w", err)
	}

	for _, c := range d.closers {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close resource: %w", err)
		}
	}

	return firstErr
}
