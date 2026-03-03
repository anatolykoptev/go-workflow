package store

import (
	"fmt"
	"time"

	workflow "github.com/anatolykoptev/go-workflow"

	"github.com/jmoiron/sqlx"
)

// StepQueue manages the step execution queue in PostgreSQL.
// Workers dequeue via SELECT ... FOR UPDATE SKIP LOCKED.
type StepQueue struct {
	db *sqlx.DB
}

// NewStepQueue connects to Postgres and returns a queue backed by the step_queue table.
func NewStepQueue(dsn string) (*StepQueue, error) {
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &StepQueue{db: db}, nil
}

// NewStepQueueFromDB wraps an existing sqlx.DB connection.
func NewStepQueueFromDB(db *sqlx.DB) *StepQueue {
	return &StepQueue{db: db}
}

// Enqueue inserts a new item into step_queue and notifies listeners.
func (q *StepQueue) Enqueue(item workflow.QueueItem) error {
	const query = `
		INSERT INTO step_queue (workflow_id, step_id, step_kind, concurrency_key, priority)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`

	err := q.db.QueryRow(query,
		item.WorkflowID, item.StepID, item.StepKind,
		nilIfEmpty(item.ConcurrencyKey), item.Priority,
	).Scan(&item.ID)
	if err != nil {
		return fmt.Errorf("enqueue step %s/%s: %w", item.WorkflowID, item.StepID, err)
	}

	_, err = q.db.Exec("SELECT pg_notify('step_enqueued', $1)", item.StepKind)
	if err != nil {
		return fmt.Errorf("notify step_enqueued: %w", err)
	}
	return nil
}

// Dequeue claims the highest-priority pending item matching the given kinds.
// Uses FOR UPDATE SKIP LOCKED to avoid contention between workers.
// Returns (nil, false) if no matching item is available.
func (q *StepQueue) Dequeue(workerID string, kinds []string) (*workflow.QueueItem, bool) {
	if len(kinds) == 0 {
		return nil, false
	}

	query, args, err := sqlx.In(
		`SELECT id, workflow_id, step_id, step_kind, priority, state, created_at
		 FROM step_queue
		 WHERE state = 'pending' AND step_kind IN (?)
		 ORDER BY priority DESC, created_at ASC
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`, kinds)
	if err != nil {
		return nil, false
	}

	tx, err := q.db.Beginx()
	if err != nil {
		return nil, false
	}
	defer tx.Rollback() //nolint:errcheck

	query = tx.Rebind(query)

	var item workflow.QueueItem
	if err := tx.Get(&item, query, args...); err != nil {
		return nil, false
	}

	now := time.Now().UTC()
	_, err = tx.Exec(
		`UPDATE step_queue SET state = 'claimed', worker_id = $1, claimed_at = $2, heartbeat_at = $2
		 WHERE id = $3`,
		workerID, now, item.ID,
	)
	if err != nil {
		return nil, false
	}

	if err := tx.Commit(); err != nil {
		return nil, false
	}

	item.State = workflow.QueueClaimed
	item.WorkerID = workerID
	item.ClaimedAt = &now
	item.HeartbeatAt = &now
	return &item, true
}

// Complete marks a queue item as done or failed and notifies listeners.
func (q *StepQueue) Complete(itemID int64, result []byte, errMsg string) error {
	state := workflow.QueueDone
	if errMsg != "" {
		state = workflow.QueueFailed
	}

	_, err := q.db.Exec(
		`UPDATE step_queue SET state = $1, result = $2, error = $3 WHERE id = $4`,
		string(state), result, nilIfEmpty(errMsg), itemID,
	)
	if err != nil {
		return fmt.Errorf("complete item %d: %w", itemID, err)
	}

	// Read workflow_id and step_id for the NOTIFY payload.
	var wfID, stepID string
	err = q.db.QueryRow(
		`SELECT workflow_id, step_id FROM step_queue WHERE id = $1`, itemID,
	).Scan(&wfID, &stepID)
	if err != nil {
		return fmt.Errorf("lookup item %d for notify: %w", itemID, err)
	}

	payload := wfID + ":" + stepID
	_, err = q.db.Exec("SELECT pg_notify('step_done', $1)", payload)
	if err != nil {
		return fmt.Errorf("notify step_done: %w", err)
	}
	return nil
}

// Fail marks a queue item as failed with the given error message.
func (q *StepQueue) Fail(itemID int64, errMsg string) error {
	return q.Complete(itemID, nil, errMsg)
}

// Heartbeat updates the heartbeat timestamp to signal the worker is alive.
func (q *StepQueue) Heartbeat(itemID int64) error {
	_, err := q.db.Exec(
		`UPDATE step_queue SET heartbeat_at = $1 WHERE id = $2`,
		time.Now().UTC(), itemID,
	)
	if err != nil {
		return fmt.Errorf("heartbeat item %d: %w", itemID, err)
	}
	return nil
}

// ReapStale resets claimed items whose heartbeat is older than timeout back to pending.
// Returns the number of reaped items.
func (q *StepQueue) ReapStale(timeout time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-timeout)
	result, err := q.db.Exec(
		`UPDATE step_queue SET state = 'pending', worker_id = NULL, claimed_at = NULL, heartbeat_at = NULL
		 WHERE state = 'claimed' AND heartbeat_at < $1`,
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("reap stale items: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// Close closes the underlying database connection.
func (q *StepQueue) Close() error {
	return q.db.Close()
}
