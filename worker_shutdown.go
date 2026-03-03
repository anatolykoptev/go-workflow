package workflow

import (
	"context"
	"time"
)

// DrainAndStop gracefully shuts down: stops accepting new work,
// waits for the current step to finish, then stops.
func (w *WorkerNode) DrainAndStop(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	w.logger.Info("draining worker", "worker_id", w.id)

	// Signal stop (no new dequeues).
	w.Stop()

	// Wait for current work to finish.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Warn("drain timeout, forcing stop", "worker_id", w.id)

			return
		case <-ticker.C:
			if w.curID.Load() == 0 { // no active work
				w.logger.Info("worker drained", "worker_id", w.id)

				return
			}
		}
	}
}
