CREATE TABLE IF NOT EXISTS workflows (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'pending',
    idempotency_key TEXT,
    data            JSONB NOT NULL,
    created_at      BIGINT NOT NULL DEFAULT 0,
    updated_at      BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_workflows_state ON workflows(state);
CREATE INDEX IF NOT EXISTS idx_workflows_owner ON workflows(owner);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflows_idempotency ON workflows(idempotency_key)
    WHERE idempotency_key IS NOT NULL
    AND state NOT IN ('completed', 'failed', 'cancelled');
