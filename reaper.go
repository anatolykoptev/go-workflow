package workflow

import (
	"context"
	"log/slog"
	"time"
)

// StepReaper is satisfied by store.StepQueue.ReapStale.
type StepReaper interface {
	ReapStale(timeout time.Duration) (int, error)
}

// Reaper periodically reclaims steps from dead workers.
type Reaper struct {
	reaper   StepReaper
	timeout  time.Duration // heartbeat timeout (e.g. 2 minutes)
	interval time.Duration // check interval (e.g. 30 seconds)
	logger   *slog.Logger
}

// NewReaper creates a reaper that checks for stale items every interval.
func NewReaper(reaper StepReaper, heartbeatTimeout, checkInterval time.Duration) *Reaper {
	return &Reaper{
		reaper:   reaper,
		timeout:  heartbeatTimeout,
		interval: checkInterval,
		logger:   slog.Default(),
	}
}

// Run starts the reaper loop. Blocks until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := r.reaper.ReapStale(r.timeout)
			if err != nil {
				r.Logger().Error("reap failed", "error", err)
				continue
			}

			if n > 0 {
				r.Logger().Warn("reaped stale items", "count", n)
			}
		}
	}
}

// Logger returns the reaper's logger.
func (r *Reaper) Logger() *slog.Logger {
	if r.logger == nil {
		return slog.Default()
	}

	return r.logger
}
