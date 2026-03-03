-- Step execution queue for distributed workers.
-- Workers dequeue via SELECT ... FOR UPDATE SKIP LOCKED.
CREATE TABLE IF NOT EXISTS step_queue (
    id              BIGSERIAL PRIMARY KEY,
    workflow_id     TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    step_id         TEXT NOT NULL,
    step_kind       TEXT NOT NULL,
    concurrency_key TEXT,
    priority        INT NOT NULL DEFAULT 0,
    state           TEXT NOT NULL DEFAULT 'pending',
    worker_id       TEXT,
    claimed_at      TIMESTAMPTZ,
    heartbeat_at    TIMESTAMPTZ,
    result          JSONB,
    error           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_step_queue_dequeue
    ON step_queue (priority DESC, created_at ASC)
    WHERE state = 'pending';

CREATE INDEX IF NOT EXISTS idx_step_queue_workflow
    ON step_queue (workflow_id, step_id);

CREATE INDEX IF NOT EXISTS idx_step_queue_worker
    ON step_queue (worker_id)
    WHERE state = 'claimed';

-- Concurrency limits: per step kind and per entity key.
CREATE TABLE IF NOT EXISTS concurrency_limits (
    key             TEXT PRIMARY KEY,
    max_concurrent  INT NOT NULL DEFAULT 0,
    current_count   INT NOT NULL DEFAULT 0
);
