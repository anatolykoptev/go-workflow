package workflow

import "time"

// QueueItemState represents the lifecycle of a queued step.
type QueueItemState string

const (
	QueuePending  QueueItemState = "pending"
	QueueClaimed  QueueItemState = "claimed"
	QueueDone     QueueItemState = "done"
	QueueFailed   QueueItemState = "failed"
	QueueTimedOut QueueItemState = "timed_out"
)

// QueueItem represents a step waiting for or being executed by a worker.
type QueueItem struct {
	ID             int64          `db:"id"`
	WorkflowID     string         `db:"workflow_id"`
	StepID         string         `db:"step_id"`
	StepKind       string         `db:"step_kind"`
	ConcurrencyKey string         `db:"concurrency_key"`
	Priority       int            `db:"priority"`
	State          QueueItemState `db:"state"`
	WorkerID       string         `db:"worker_id"`
	ClaimedAt      *time.Time     `db:"claimed_at"`
	HeartbeatAt    *time.Time     `db:"heartbeat_at"`
	Result         []byte         `db:"result"`
	Error          string         `db:"error"`
	CreatedAt      time.Time      `db:"created_at"`
}

// ConcurrencyLimit defines a concurrency constraint.
type ConcurrencyLimit struct {
	Key           string `db:"key"`
	MaxConcurrent int    `db:"max_concurrent"`
	CurrentCount  int    `db:"current_count"`
}
